package event

import (
	"context"
	"encoding/json"
	"log"

	"gitflex.diasoft.ru/mvp-go/golang-libraries/go-kafka.git/kafka"
	"github.com/nx-a/conv"
	"github.com/nx-a/fsearch"
)

// defaultSearchWorkers — размер пула воркеров, обрабатывающих search-command.
// Переопределяется переменной окружения SEARCH_WORKERS.
const defaultSearchWorkers = 1024

type job struct {
	ctx context.Context
	m   kafka.Message
}

type In struct {
	out    *Out
	engine *fsearch.Engine
	jobs   chan job
}

func NewIn(eng *fsearch.Engine) *In {
	workers := envInt("SEARCH_WORKERS", defaultSearchWorkers)
	in := &In{
		out:    NewOut(),
		engine: eng,
		jobs:   make(chan job, workers),
	}
	for i := 0; i < workers; i++ {
		go in.worker()
	}
	ctx := context.Background()
	cfg := kafka.NewReaderConfig("search-command", "fsearch-12", kafka.WithOffset(kafka.FirstOffset))
	if err := kafka.RunListenerWithReaderConfig(ctx, in.search, cfg); err != nil {
		log.Printf("failed to start search-command listener: %e", err)
	}
	return in
}

// worker последовательно разбирает задания из общего пула.
func (in *In) worker() {
	for j := range in.jobs {
		in.query(j.ctx, j.m)
	}
}

type Query struct {
	ListSysName string   `json:"sysName"`
	Fields      []Fields `json:"fields"`
}
type Fields struct {
	Id    int    `json:"id"`
	Field string `json:"field"`
	Value string `json:"value"`
}
type Response struct {
	Id      int              `json:"id"`
	Results []fsearch.Result `json:"results"`
	Error   string           `json:"error"`
}

func (in *In) search(ctx context.Context, m kafka.Message) error {
	// Блокирующая передача в пул создаёт backpressure: при насыщении
	// воркеров чтение из топика приостанавливается вместо порождения
	// неограниченного числа горутин.
	in.jobs <- job{ctx: ctx, m: m}
	return nil
}
func (in *In) query(ctx context.Context, m kafka.Message) {
	var query Query
	if json.Unmarshal(m.Value, &query) != nil {
		log.Println("failed to unmarshal query")
		return
	}
	if query.ListSysName == "" {
		log.Println("listSysname is required")
		return
	}
	if len(query.Fields) == 0 {
		log.Println("fields is required")
		return
	}
	responses := make([]Response, 0, len(query.Fields))
	for _, field := range query.Fields {
		if field.Field == "" || field.Value == "" {
			responses = append(responses, Response{Id: field.Id, Error: "field and value are required"})
			continue
		}
		res, err := in.engine.Search(query.ListSysName, field.Field, field.Value, 0)
		if err != nil {
			responses = append(responses, Response{Id: field.Id, Error: err.Error()})
			continue
		}
		responses = append(responses, Response{Id: field.Id, Results: res})
	}
	in.reply(ctx, []byte(conv.JSON(responses)), m.Headers)
}
func (in *In) reply(ctx context.Context, resp []byte, headers []kafka.Header) {
	err := in.out.WriteMessage(ctx, "search-reply", resp, headers)
	if err != nil {
		log.Printf("Error send reply: %e", err)
	}
}
