// Команда fsearch — небольшой CLI вокруг движка fsearch: создание списков,
// добавление/импорт плоских JSON-записей, нечёткий поиск и HTTP-сервер.
//
// Использование:
//
//	fsearch create -db data.db -list users -fields name,email
//	fsearch add    -db data.db -list users -json '{"name":"Иванов Иван"}'
//	fsearch import -db data.db -list users -file records.json   # JSON-массив
//	fsearch search -db data.db -list users -field name -q ivanov -limit 10
//	fsearch lists  -db data.db
//	fsearch serve  -db data.db -addr :8080
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/nx-a/fsearch"
)

func main() {
	os.Args = append(os.Args, "search")
	os.Args = append(os.Args, "")
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "parse":
		err = cmdParse(args)
	case "create":
		err = cmdCreate(args)
	case "add":
		err = cmdAdd(args)
	case "import":
		err = cmdImport(args)
	case "search":
		err = cmdSearch(args)
	case "lists":
		err = cmdLists(args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func cmdParse(args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	db := fs.String("db", "fsearch.db", "database file")
	list := fs.String("list", "", "list sysname")
	file := fs.String("file", "", "path to JSON array of records (stdin if omitted)")
	fs.Parse(args)
	if *list == "" {
		return fmt.Errorf("-list is required")
	}
	e, err := openEngine(*db)
	if err != nil {
		return err
	}
	_, err = ProcessLargeXML(*file, e, *list)
	if err != nil {
		log.Fatal(err)
	}
	defer e.Close()
	return nil
}

func usage() {
	fmt.Fprint(os.Stderr, `fsearch - fuzzy search for flat JSON records

Commands:
  create  -db FILE -list NAME -fields f1,f2     create a list
  add     -db FILE -list NAME -json '{...}'      add one record (or stdin)
  import  -db FILE -list NAME -file records.json import a JSON array of records
  search  -db FILE -list NAME -field F -q TERM   fuzzy search (min 5 chars)
  lists   -db FILE                               list sysnames
  serve   -db FILE -addr :8080                   start the HTTP REST server
`)
}

func openEngine(path string) (*fsearch.Engine, error) {
	return fsearch.Open(fsearch.Options{Path: path})
}

func cmdCreate(args []string) error {
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	db := fs.String("db", "fsearch.db", "database file")
	list := fs.String("list", "", "list sysname")
	fields := fs.String("fields", "", "comma-separated searchable fields")
	fs.Parse(args)
	if *list == "" {
		return fmt.Errorf("-list is required")
	}
	var f []string
	for _, p := range strings.Split(*fields, ",") {
		if p = strings.TrimSpace(p); p != "" {
			f = append(f, p)
		}
	}
	e, err := openEngine(*db)
	if err != nil {
		return err
	}
	defer e.Close()
	if err := e.CreateList(*list, f); err != nil {
		return err
	}
	fmt.Printf("created list %q with fields %v\n", *list, f)
	return nil
}

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	db := fs.String("db", "fsearch.db", "database file")
	list := fs.String("list", "", "list sysname")
	jsonStr := fs.String("json", "", "record JSON (reads stdin if omitted)")
	fs.Parse(args)
	if *list == "" {
		return fmt.Errorf("-list is required")
	}
	raw := []byte(*jsonStr)
	if len(raw) == 0 {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		raw = b
	}
	e, err := openEngine(*db)
	if err != nil {
		return err
	}
	defer e.Close()
	id, err := e.AddJSON(*list, raw)
	if err != nil {
		return err
	}
	fmt.Printf("added record id=%d\n", id)
	return nil
}

func cmdImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	db := fs.String("db", "fsearch.db", "database file")
	list := fs.String("list", "", "list sysname")
	file := fs.String("file", "", "path to JSON array of records (stdin if omitted)")
	fs.Parse(args)
	if *list == "" {
		return fmt.Errorf("-list is required")
	}
	var r io.Reader = os.Stdin
	if *file != "" {
		f, err := os.Open(*file)
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}
	var records []map[string]any
	if err := json.NewDecoder(r).Decode(&records); err != nil {
		return fmt.Errorf("decode records array: %w", err)
	}
	e, err := openEngine(*db)
	if err != nil {
		return err
	}
	defer e.Close()
	for i, rec := range records {
		if _, err := e.AddRecord(*list, rec); err != nil {
			return fmt.Errorf("record %d: %w", i, err)
		}
	}
	fmt.Printf("imported %d records into %q\n", len(records), *list)
	return nil
}

func cmdSearch(args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	db := fs.String("db", "fsearch.db", "database file")
	list := fs.String("list", "black", "list sysname")
	field := fs.String("field", "ФЛ.Фамилия", "field to search")
	q := fs.String("q", "АЗАМАТ", "query (min 5 characters)")
	limit := fs.Int("limit", 10, "max results")
	fs.Parse(args)
	if *list == "" || *field == "" || *q == "" {
		return fmt.Errorf("-list, -field and -q are required")
	}
	e, err := openEngine(*db)
	if err != nil {
		return err
	}
	defer e.Close()
	results, err := e.Search(*list, *field, *q, *limit)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}

func cmdLists(args []string) error {
	fs := flag.NewFlagSet("lists", flag.ExitOnError)
	db := fs.String("db", "fsearch.db", "database file")
	fs.Parse(args)
	e, err := openEngine(*db)
	if err != nil {
		return err
	}
	defer e.Close()
	names, err := e.Lists()
	if err != nil {
		return err
	}
	for _, n := range names {
		fmt.Println(n)
	}
	return nil
}
