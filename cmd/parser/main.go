package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
)

func main() {
	filePath := os.Args[1]
	subjects, err := ProcessLargeXML(filePath)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Загружено %d субъектов\n", len(subjects))

	// Вывод примера
	if len(subjects) > 0 {
		prettyJSON, _ := json.MarshalIndent(subjects[0], "", "  ")
		fmt.Println("Пример субъекта:")
		fmt.Println(string(prettyJSON))
	}
}
