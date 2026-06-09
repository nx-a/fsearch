package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
)

type XMLToFlatMapper struct {
	decoder *xml.Decoder
}

func NewXMLToFlatMapper(r io.Reader) *XMLToFlatMapper {
	return &XMLToFlatMapper{
		decoder: xml.NewDecoder(r),
	}
}

// ReadNextSubject читает следующего субъекта и возвращает плоскую map
func (m *XMLToFlatMapper) ReadNextSubject() (map[string]interface{}, error) {
	for {
		token, err := m.decoder.Token()
		if err != nil {
			return nil, err
		}

		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Local == "Субъект" {
				return m.parseSubject()
			}
		}
	}
}

func (m *XMLToFlatMapper) parseSubject() (map[string]interface{}, error) {
	result := make(map[string]interface{})

	for {
		token, err := m.decoder.Token()
		if err != nil {
			return nil, err
		}

		switch t := token.(type) {
		case xml.StartElement:
			m.parseElement(t, result, t.Name.Local)

		case xml.EndElement:
			if t.Name.Local == "Субъект" {
				return result, nil
			}
		}
	}
}

func (m *XMLToFlatMapper) parseElement(start xml.StartElement, result map[string]interface{}, prefix string) {
	var content strings.Builder

	for {
		token, err := m.decoder.Token()
		if err != nil {
			return
		}

		switch t := token.(type) {
		case xml.StartElement:
			// Вложенный элемент
			newPrefix := prefix + "." + t.Name.Local
			m.parseElement(t, result, newPrefix)

		case xml.CharData:
			content.WriteString(strings.TrimSpace(string(t)))

		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				text := content.String()
				if text != "" {
					result[prefix] = text
				}
				return
			}
		}
	}
}

// ProcessLargeXML - основной процессор для больших файлов
func ProcessLargeXML(filename string) ([]map[string]interface{}, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("ошибка открытия: %w", err)
	}
	defer file.Close()

	results := make([]map[string]interface{}, 0)
	mapper := NewXMLToFlatMapper(file)

	for {
		subject, err := mapper.ReadNextSubject()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("Ошибка парсинга: %v", err)
			continue
		}
		results = append(results, subject)

		// Прогресс для больших файлов
		if len(results)%1000 == 0 {
			log.Printf("Обработано %d субъектов", len(results))
		}
	}

	return results, nil
}
