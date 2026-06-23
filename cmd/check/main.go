// Утилита check: извлекает ФЛ.Фамилия из XML-перечня, формирует поисковые запросы,
// отправляет их в топик search-command, получает ответы из search-reply и
// замеряет производительность (пропускную способность и латентность).
package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/nx-a/fsearch/internal/event"

	"gitflex.diasoft.ru/mvp-go/golang-libraries/go-kafka.git/kafka"
)

const (
	commandTopic = "search-command"
	replyTopic   = "search-reply"
	corrHeader   = "x-check-corr"
)

type config struct {
	file        string
	sysName     string
	field       string
	batch       int
	limit       int
	concurrency int
	warmup      time.Duration
	timeout     time.Duration
}

func main() {
	cfg := parseFlags()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	names, err := extractNames(cfg.file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if cfg.limit > 0 && cfg.limit < len(names) {
		names = names[:cfg.limit]
	}
	if len(names) == 0 {
		fmt.Fprintln(os.Stderr, "error: в файле не найдено ни одного ФЛ.Фамилия")
		os.Exit(1)
	}
	log.Printf("извлечено ФЛ.Фамилия: %d (поле=%q, sysName=%q, batch=%d)",
		len(names), cfg.field, cfg.sysName, cfg.batch)

	if err := run(ctx, cfg, names); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.file, "file", "20.04.2026-v 2.2.xml", "путь к XML-файлу перечня")
	flag.StringVar(&cfg.sysName, "sysname", env("SERVICE_NAME", "black"), "sysName списка для поиска")
	flag.StringVar(&cfg.field, "field", "ФЛ.Фамилия", "имя поискового поля")
	flag.IntVar(&cfg.batch, "batch", 1, "сколько Фамилия упаковывать в одно сообщение search-command")
	flag.IntVar(&cfg.limit, "n", 0, "ограничить число Фамилия (0 — все)")
	flag.IntVar(&cfg.concurrency, "concurrency", 64, "число параллельных отправителей в брокер (нагрузка)")
	flag.DurationVar(&cfg.warmup, "warmup", 3*time.Second, "пауза перед отправкой, чтобы консьюмер подключился")
	flag.DurationVar(&cfg.timeout, "timeout", 60*time.Minute, "максимальное время ожидания ответов")
	flag.Parse()
	return cfg
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// extractNames потоково читает XML и собирает все непустые ФЛ.Фамилия.
func extractNames(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("открытие файла: %w", err)
	}
	defer f.Close()

	type subject struct {
		FIO string `xml:"ФЛ>Фамилия"`
	}

	dec := xml.NewDecoder(f)
	names := make([]string, 0, 1024)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("разбор XML: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "Субъект" {
			continue
		}
		var s subject
		if err := dec.DecodeElement(&s, &se); err != nil {
			return nil, fmt.Errorf("декодирование субъекта: %w", err)
		}
		if s.FIO != "" {
			names = append(names, s.FIO)
		}
	}
	return names, nil
}

type command struct {
	corr    string
	payload []byte
	count   int
}

type pending struct {
	sentAt time.Time
	count  int
}

func buildCommands(cfg config, names []string) ([]command, error) {
	commands := make([]command, 0, (len(names)+cfg.batch-1)/cfg.batch)
	for start := 0; start < len(names); start += cfg.batch {
		end := min(start+cfg.batch, len(names))
		fields := make([]event.Fields, 0, end-start)
		for i := start; i < end; i++ {
			fields = append(fields, event.Fields{Id: i, Field: cfg.field, Value: names[i]})
		}
		payload, err := json.Marshal(event.Query{ListSysName: cfg.sysName, Fields: fields})
		if err != nil {
			return nil, fmt.Errorf("маршалинг запроса: %w", err)
		}
		commands = append(commands, command{corr: uuid.NewString(), payload: payload, count: end - start})
	}
	return commands, nil
}

func run(ctx context.Context, cfg config, names []string) error {
	commands, err := buildCommands(cfg, names)
	if err != nil {
		return err
	}

	var mu sync.Mutex
	pendings := make(map[string]pending, len(commands))
	latencies := make([]time.Duration, 0, len(commands))
	var repliesReceived, responsesReceived, found, searchErrors int
	done := make(chan struct{})

	consumerCtx, cancelConsumer := context.WithCancel(ctx)
	defer cancelConsumer()

	handler := func(_ context.Context, m kafka.Message) error {
		corr := headerValue(m.Headers, corrHeader)
		if corr == "" {
			return nil
		}
		now := time.Now()

		mu.Lock()
		p, ok := pendings[corr]
		if !ok {
			mu.Unlock()
			return nil
		}
		delete(pendings, corr)
		var responses []event.Response
		if err = json.Unmarshal(m.Value, &responses); err != nil {
			mu.Unlock()
			return fmt.Errorf("разбор ответа: %w", err)
		}
		repliesReceived++
		responsesReceived += len(responses)
		for _, r := range responses {
			if r.Error != "" {
				searchErrors++
				continue
			}
			if len(r.Results) > 0 {
				found++
			}
		}
		latencies = append(latencies, now.Sub(p.sentAt))
		remaining := len(pendings)
		mu.Unlock()

		if remaining == 0 {
			select {
			case <-done:
			default:
				close(done)
			}
		}
		return nil
	}

	group := "fsearch-check-" + uuid.NewString()
	kafka.RunListener(consumerCtx, handler, replyTopic, group)

	log.Printf("ожидание подключения консьюмера: %s", cfg.warmup)
	select {
	case <-time.After(cfg.warmup):
	case <-ctx.Done():
		return ctx.Err()
	}

	out := event.NewOut()
	workers := cfg.concurrency
	if workers < 1 {
		workers = 1
	}
	log.Printf("отправка %d сообщений (%d Фамилия) в топик %s, параллельно=%d...",
		len(commands), len(names), commandTopic, workers)
	startSend := time.Now()

	jobs := make(chan command)
	var sendWg sync.WaitGroup
	var sendErr error
	var sendErrOnce sync.Once
	for i := 0; i < workers; i++ {
		sendWg.Add(1)
		go func() {
			defer sendWg.Done()
			for c := range jobs {
				headers := []kafka.Header{{Key: corrHeader, Value: []byte(c.corr)}}
				mu.Lock()
				pendings[c.corr] = pending{sentAt: time.Now(), count: c.count}
				mu.Unlock()
				if err := out.WriteMessage(ctx, commandTopic, c.payload, headers); err != nil {
					sendErrOnce.Do(func() { sendErr = fmt.Errorf("отправка в %s: %w", commandTopic, err) })
					mu.Lock()
					delete(pendings, c.corr)
					mu.Unlock()
				}
			}
		}()
	}
	for _, c := range commands {
		jobs <- c
	}
	close(jobs)
	sendWg.Wait()
	if sendErr != nil {
		return sendErr
	}
	sendDuration := time.Since(startSend)
	log.Printf("все сообщения отправлены за %s, ожидание ответов...", sendDuration.Round(time.Millisecond))

	timer := time.NewTimer(cfg.timeout)
	defer timer.Stop()
	timedOut := false
	select {
	case <-done:
	case <-timer.C:
		timedOut = true
	case <-ctx.Done():
		return ctx.Err()
	}
	totalDuration := time.Since(startSend)
	cancelConsumer()

	mu.Lock()
	defer mu.Unlock()
	printReport(report{
		names:             len(names),
		commands:          len(commands),
		batch:             cfg.batch,
		sendDuration:      sendDuration,
		totalDuration:     totalDuration,
		repliesReceived:   repliesReceived,
		responsesReceived: responsesReceived,
		found:             found,
		searchErrors:      searchErrors,
		missing:           len(pendings),
		timedOut:          timedOut,
		latencies:         latencies,
	})
	return nil
}

func headerValue(headers []kafka.Header, key string) string {
	for _, h := range headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

type report struct {
	names             int
	commands          int
	batch             int
	sendDuration      time.Duration
	totalDuration     time.Duration
	repliesReceived   int
	responsesReceived int
	found             int
	searchErrors      int
	missing           int
	timedOut          bool
	latencies         []time.Duration
}

func printReport(r report) {
	fmt.Println()
	fmt.Println("================ Результаты замера ================")
	fmt.Printf("Фамилия отправлено:        %d\n", r.names)
	fmt.Printf("Сообщений (команд):    %d (batch=%d)\n", r.commands, r.batch)
	fmt.Printf("Получено ответов:      %d / %d\n", r.repliesReceived, r.commands)
	fmt.Printf("Ответов по полям:      %d / %d\n", r.responsesReceived, r.names)
	fmt.Printf("Найдено (есть совп.):  %d\n", r.found)
	fmt.Printf("Ошибок поиска:         %d\n", r.searchErrors)
	if r.missing > 0 {
		fmt.Printf("Не получено ответов:   %d\n", r.missing)
	}
	if r.timedOut {
		fmt.Println("ВНИМАНИЕ: достигнут таймаут ожидания ответов")
	}
	fmt.Println("---------------------------------------------------")
	fmt.Printf("Время отправки:        %s\n", r.sendDuration.Round(time.Millisecond))
	fmt.Printf("Полное время:          %s\n", r.totalDuration.Round(time.Millisecond))
	if r.totalDuration > 0 {
		fmt.Printf("Пропускная способность: %.1f Фамилия/сек, %.1f сообщ./сек\n",
			float64(r.responsesReceived)/r.totalDuration.Seconds(),
			float64(r.repliesReceived)/r.totalDuration.Seconds())
	}
	printLatency(r.latencies)
	fmt.Println("===================================================")
}

func printLatency(latencies []time.Duration) {
	if len(latencies) == 0 {
		fmt.Println("Латентность:           нет данных")
		return
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	var sum time.Duration
	for _, l := range latencies {
		sum += l
	}
	avg := sum / time.Duration(len(latencies))
	fmt.Println("Латентность (round-trip, на сообщение):")
	fmt.Printf("  min=%s  avg=%s  max=%s\n",
		latencies[0].Round(time.Microsecond),
		avg.Round(time.Microsecond),
		latencies[len(latencies)-1].Round(time.Microsecond))
	fmt.Printf("  p50=%s  p95=%s  p99=%s\n",
		percentile(latencies, 0.50).Round(time.Microsecond),
		percentile(latencies, 0.95).Round(time.Microsecond),
		percentile(latencies, 0.99).Round(time.Microsecond))
}

// percentile возвращает значение перцентиля p (0..1) из отсортированного среза.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
