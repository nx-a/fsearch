// Пакет httpapi предоставляет REST-обёртку над движком fsearch: управление
// списками (создание, просмотр, удаление) и записями (добавление, чтение,
// обновление, удаление), а также нечёткий поиск.
//
// Базовый сегмент путей задаётся каналом-префиксом (ChannelPrefix); ниже он
// обозначен как {p}. Маршруты:
//
//	GET    /{p}                         список sysname
//	POST   /{p}                         создать список {sysname, search_fields}
//	GET    /{p}/{sysname}               метаданные списка
//	DELETE /{p}/{sysname}               удалить список
//	POST   /{p}/{sysname}/records       добавить запись (объект) или записи (массив)
//	GET    /{p}/{sysname}/records/{id}  получить запись
//	PUT    /{p}/{sysname}/records/{id}  обновить запись
//	DELETE /{p}/{sysname}/records/{id}  удалить запись
//	GET    /{p}/{sysname}/search?field=&q=&limit=   нечёткий поиск
//
// Документация API доступна как Swagger UI на /swagger-ui, спецификация
// OpenAPI — на /openapi.yaml (формируется налёту с учётом префикса).
//
// Роли (см. Config.IsReadOnly):
//   - писатель: все маршруты + GET /snapshot (консистентный снапшот базы);
//   - читатель: только операции чтения/поиска; запись и снапшот не регистрируются.
//
// Служебные эндпоинты: GET /healthz (liveness) и GET /readyz (readiness).
package httpapi

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync/atomic"
	"text/template"
	"time"

	"github.com/nx-a/fsearch"
)

//go:embed openapi.yaml
var openAPITemplate string

// swaggerUIHTML — страница Swagger UI, подгружающая ассеты с CDN и
// спецификацию с /openapi.yaml.
const swaggerUIHTML = `<!DOCTYPE html>
<html lang="ru">
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1"/>
  <title>fsearch API</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist/swagger-ui.css"/>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist/swagger-ui-bundle.js"></script>
  <script>
    window.onload = function () {
      window.ui = SwaggerUIBundle({ url: '/openapi.yaml', dom_id: '#swagger-ui' });
    };
  </script>
</body>
</html>`

// Server — HTTP-обработчик поверх движка fsearch.
//
// Движок хранится в atomic.Pointer, чтобы под-читатель мог «на горячую»
// подменять его свежим снапшотом без остановки сервера.
type Server struct {
	engine   atomic.Pointer[fsearch.Engine]
	mux      *http.ServeMux
	spec     []byte
	readOnly bool
	ready    atomic.Bool
}

// Config описывает то, что серверу нужно знать о развёртывании: префикс путей
// и роль (читатель/писатель).
type Config interface {
	GetServiceContext() string
	IsReadOnly() bool
}

// NewServer создаёт обработчик с зарегистрированными маршрутами. При роли
// читателя (cfg.IsReadOnly()) регистрируются только операции чтения/поиска.
func NewServer(engine *fsearch.Engine, cfg Config) *Server {
	s := &Server{mux: http.NewServeMux(), readOnly: cfg.IsReadOnly()}
	s.engine.Store(engine)
	if engine != nil {
		s.ready.Store(true)
	}
	s.spec = renderSpec(cfg.GetServiceContext())
	s.routes(cfg)
	return s
}

// eng возвращает текущий движок.
func (s *Server) eng() *fsearch.Engine { return s.engine.Load() }

// SetEngine атомарно подменяет движок (используется читателем после загрузки
// нового снапшота) и помечает сервер готовым. Прежний движок закрывается с
// задержкой, чтобы не оборвать незавершённые запросы.
func (s *Server) SetEngine(e *fsearch.Engine) {
	old := s.engine.Swap(e)
	s.ready.Store(true)
	if old != nil && old != e {
		time.AfterFunc(30*time.Second, func() { _ = old.Close() })
	}
}

// renderSpec формирует спецификацию OpenAPI налёту, подставляя в пути тот же
// префикс, что используется при регистрации маршрутов. Так Swagger всегда
// соответствует фактическим эндпоинтам и не требует ручной правки.
func renderSpec(prefix string) []byte {
	base := "/" + prefix
	data := map[string]string{
		"List":    base,
		"Item":    base + "/{sysname}",
		"Records": base + "/{sysname}/records",
		"Record":  base + "/{sysname}/records/{id}",
		"Search":  base + "/{sysname}/search",
	}
	tmpl, err := template.New("openapi").Parse(openAPITemplate)
	if err != nil {
		return []byte(openAPITemplate)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return []byte(openAPITemplate)
	}
	return buf.Bytes()
}

// ServeHTTP реализует http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes(cfg Config) {
	p := cfg.GetServiceContext()

	// Служебные и документация — доступны всегда.
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /readyz", s.handleReadyz)
	s.mux.HandleFunc("GET /swagger-ui", s.handleSwaggerUI)
	s.mux.HandleFunc("GET /openapi.yaml", s.handleOpenAPISpec)

	// Операции чтения — доступны и читателям, и писателю.
	s.mux.HandleFunc("GET /"+p, s.handleListLists)
	s.mux.HandleFunc("GET /"+p+"/{sysname}", s.handleGetList)
	s.mux.HandleFunc("GET /"+p+"/{sysname}/records/{id}", s.handleGetRecord)
	s.mux.HandleFunc("GET /"+p+"/{sysname}/search", s.handleSearch)

	if s.readOnly {
		// Под-читатель: запись недоступна, снапшот не отдаём.
		return
	}

	// Снапшот для подов-читателей (только у писателя).
	s.mux.HandleFunc("GET /snapshot", s.handleSnapshot)

	// Операции записи — только у писателя.
	s.mux.HandleFunc("POST /"+p, s.handleCreateList)
	s.mux.HandleFunc("DELETE /"+p+"/{sysname}", s.handleDeleteList)
	s.mux.HandleFunc("POST /"+p+"/{sysname}/records", s.handleAddRecords)
	s.mux.HandleFunc("PUT /"+p+"/{sysname}/records/{id}", s.handleUpdateRecord)
	s.mux.HandleFunc("DELETE /"+p+"/{sysname}/records/{id}", s.handleDeleteRecord)
}

// handleHealthz — liveness-проба.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleReadyz — readiness-проба. Читатель не готов, пока не загрузил хотя бы
// один снапшот.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() || s.eng() == nil {
		writeStatus(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready"))
}

// handleSnapshot отдаёт консистентный снапшот базы (writer-only).
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="fsearch-snapshot.db"`)
	if _, err := s.eng().Snapshot(w); err != nil {
		// Заголовки уже могли быть отправлены — обрываем соединение.
		panic(http.ErrAbortHandler)
	}
}

// handleSwaggerUI отдаёт страницу Swagger UI.
func (s *Server) handleSwaggerUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(swaggerUIHTML))
}

// handleOpenAPISpec отдаёт спецификацию OpenAPI в формате YAML.
func (s *Server) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_, _ = w.Write(s.spec)
}

// createListRequest — тело запроса на создание списка.
type createListRequest struct {
	Sysname      string   `json:"sysname"`
	SearchFields []string `json:"search_fields"`
}

func (s *Server) handleListLists(w http.ResponseWriter, r *http.Request) {
	names, err := s.eng().Lists()
	if err != nil {
		writeError(w, err)
		return
	}
	if names == nil {
		names = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"lists": names})
}

func (s *Server) handleCreateList(w http.ResponseWriter, r *http.Request) {
	var req createListRequest
	if err := decodeBody(r, &req); err != nil {
		writeStatus(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Sysname == "" {
		writeStatus(w, http.StatusBadRequest, "sysname is required")
		return
	}
	if err := s.eng().CreateList(req.Sysname, req.SearchFields); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"sysname":       req.Sysname,
		"search_fields": req.SearchFields,
	})
}

func (s *Server) handleGetList(w http.ResponseWriter, r *http.Request) {
	meta, err := s.eng().ListMeta(r.PathValue("sysname"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

func (s *Server) handleDeleteList(w http.ResponseWriter, r *http.Request) {
	if err := s.eng().DeleteList(r.PathValue("sysname")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAddRecords(w http.ResponseWriter, r *http.Request) {
	sysname := r.PathValue("sysname")
	raw, err := readBody(r)
	if err != nil {
		writeStatus(w, http.StatusBadRequest, err.Error())
		return
	}
	// Поддерживаем как один объект, так и массив объектов.
	if isJSONArray(raw) {
		var records []map[string]any
		if err := json.Unmarshal(raw, &records); err != nil {
			writeStatus(w, http.StatusBadRequest, "invalid JSON array: "+err.Error())
			return
		}
		ids := make([]uint64, 0, len(records))
		for _, rec := range records {
			id, err := s.eng().AddRecord(sysname, rec)
			if err != nil {
				writeError(w, err)
				return
			}
			ids = append(ids, id)
		}
		writeJSON(w, http.StatusCreated, map[string]any{"ids": ids})
		return
	}

	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		writeStatus(w, http.StatusBadRequest, "invalid JSON object: "+err.Error())
		return
	}
	id, err := s.eng().AddRecord(sysname, record)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) handleGetRecord(w http.ResponseWriter, r *http.Request) {
	sysname := r.PathValue("sysname")
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeStatus(w, http.StatusBadRequest, err.Error())
		return
	}
	rec, ok, err := s.eng().GetRecord(sysname, id)
	if err != nil {
		writeError(w, err)
		return
	}
	if !ok {
		writeStatus(w, http.StatusNotFound, "record not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "record": rec})
}

func (s *Server) handleUpdateRecord(w http.ResponseWriter, r *http.Request) {
	sysname := r.PathValue("sysname")
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeStatus(w, http.StatusBadRequest, err.Error())
		return
	}
	var record map[string]any
	if err := decodeBody(r, &record); err != nil {
		writeStatus(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.eng().UpdateRecord(sysname, id, record); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "record": record})
}

func (s *Server) handleDeleteRecord(w http.ResponseWriter, r *http.Request) {
	sysname := r.PathValue("sysname")
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeStatus(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.eng().DeleteRecord(sysname, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	sysname := r.PathValue("sysname")
	q := r.URL.Query()
	field := q.Get("field")
	query := q.Get("q")
	if field == "" || query == "" {
		writeStatus(w, http.StatusBadRequest, "query params 'field' and 'q' are required")
		return
	}
	limit := 10
	if l := q.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil {
			writeStatus(w, http.StatusBadRequest, "invalid 'limit'")
			return
		}
		limit = n
	}
	results, err := s.eng().Search(sysname, field, query, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	if results == nil {
		results = []fsearch.Result{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// writeError сопоставляет ошибки движка с HTTP-статусами.
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fsearch.ErrListNotFound), errors.Is(err, fsearch.ErrRecordNotFound):
		writeStatus(w, http.StatusNotFound, err.Error())
	case errors.Is(err, fsearch.ErrListExists):
		writeStatus(w, http.StatusConflict, err.Error())
	case errors.Is(err, fsearch.ErrQueryTooShort):
		writeStatus(w, http.StatusBadRequest, err.Error())
	default:
		writeStatus(w, http.StatusInternalServerError, err.Error())
	}
}

func writeStatus(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}
