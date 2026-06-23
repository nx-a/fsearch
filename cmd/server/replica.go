package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/nx-a/fsearch"
	"github.com/nx-a/fsearch/httpapi"
)

// runReader запускает под-читатель: периодически тянет снапшот у писателя,
// открывает его в режиме read-only и «на горячую» подменяет движок в сервере.
// Перед началом обслуживания выполняется первичная синхронизация (с повторами).
func runReader(ctx context.Context, conf *Config) error {
	if conf.GetWriterURL() == "" {
		return errors.New("reader: WRITER_URL is required")
	}
	dir := snapshotDir(conf)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Первичная синхронизация: ждём, пока писатель отдаст снапшот.
	curPath, eng, err := fetchAndOpen(ctx, conf, dir)
	for err != nil {
		log.Printf("reader: первичная синхронизация не удалась: %v (повтор через %s)", err, conf.GetSyncInterval())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(conf.GetSyncInterval()):
		}
		curPath, eng, err = fetchAndOpen(ctx, conf, dir)
	}

	srv := httpapi.NewServer(eng, conf)
	log.Printf("fsearch читатель слушает :%s (источник: %s)", conf.ServicePort, conf.GetWriterURL())

	go syncLoop(ctx, conf, dir, srv, curPath)
	return serve(ctx, conf, srv)
}

// syncLoop периодически обновляет снапшот и подменяет движок.
func syncLoop(ctx context.Context, conf *Config, dir string, srv *httpapi.Server, curPath string) {
	ticker := time.NewTicker(conf.GetSyncInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			newPath, eng, err := fetchAndOpen(ctx, conf, dir)
			if err != nil {
				log.Printf("reader: синхронизация снапшота не удалась: %v", err)
				continue
			}
			srv.SetEngine(eng)
			// Старый файл снапшота удаляем с задержкой — движок ещё закрывается.
			old := curPath
			curPath = newPath
			time.AfterFunc(45*time.Second, func() { _ = os.Remove(old) })
		}
	}
}

// fetchAndOpen скачивает снапшот у писателя во временный файл и открывает его
// движком только на чтение.
func fetchAndOpen(ctx context.Context, conf *Config, dir string) (string, *fsearch.Engine, error) {
	path, err := fetchSnapshot(ctx, conf, dir)
	if err != nil {
		return "", nil, err
	}
	eng, err := fsearch.Open(fsearch.Options{Path: path, ReadOnly: true})
	if err != nil {
		_ = os.Remove(path)
		return "", nil, err
	}
	return path, eng, nil
}

// fetchSnapshot выполняет GET <writer>/snapshot и сохраняет тело в новый файл.
func fetchSnapshot(ctx context.Context, conf *Config, dir string) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, conf.GetWriterURL()+"/snapshot", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("snapshot: unexpected status %d", resp.StatusCode)
	}

	path := filepath.Join(dir, fmt.Sprintf("snapshot-%d.db", time.Now().UnixNano()))
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// snapshotDir — каталог для локальных копий снапшота читателя.
func snapshotDir(conf *Config) string {
	d := filepath.Dir(conf.Db)
	if d == "" || d == "." {
		return "snapshots"
	}
	return filepath.Join(d, "snapshots")
}
