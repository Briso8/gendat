package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/v2fly/v2ray-core/v5/app/router/routercommon"
	"google.golang.org/protobuf/proto"
)

type Config struct {
	GeoSite map[string][]string `json:"geosite"`
}

func main() {
	dataDir := flag.String("data", "./external-domains/data", "Путь к клонированной папке data")
	nashDir := flag.String("nash", "./nash", "Путь к локальной папке с кастомными доменами")
	configFile := flag.String("config", "config.json", "Конфигурационный файл со ссылками")
	flag.Parse()

	categories := make(map[string]map[string]struct{})

	// 1. Читаем внешнюю папку data
	log.Println("Чтение внешней папки data...")
	readDirectory(*dataDir, categories, *dataDir)

	// 2. Читаем локальную папку nash
	log.Println("Чтение локальной папки nash...")
	readDirectory(*nashDir, categories, *nashDir)

	// 3. Читаем домены по URL из config.json
	log.Println("Чтение ссылок из config.json...")
	configBytes, err := os.ReadFile(*configFile)
	if err == nil {
		var cfg Config
		if err := json.Unmarshal(configBytes, &cfg); err != nil {
			log.Printf("Ошибка парсинга %s: %v\n", *configFile, err)
		} else {
			for category, urls := range cfg.GeoSite {
				if categories[category] == nil {
					categories[category] = make(map[string]struct{})
				}
				for _, u := range urls {
					lines := fetchLines(u)
					for _, line := range lines {
						line = strings.TrimSpace(line)
						if line == "" || strings.HasPrefix(line, "#") {
							continue
						}
						// Используем специальную очистку ТОЛЬКО для файлов из config.json
						domain := cleanRemoteDomain(line)
						if domain != "" {
							categories[category][domain] = struct{}{}
						}
					}
				}
			}
		}
	} else {
		log.Printf("Предупреждение: не удалось прочитать %s: %v\n", *configFile, err)
	}

	// 4. Сборка итогового geosite.dat
	log.Println("Сборка geosite.dat...")
	var geositeList routercommon.GeoSiteList

	for category, domains := range categories {
		var domainEntries []*routercommon.Domain
		for d := range domains {
			domainEntries = append(domainEntries, &routercommon.Domain{
				Type:  routercommon.Domain_RootDomain,
				Value: d,
			})
		}

		geositeList.Entry = append(geositeList.Entry, &routercommon.GeoSite{
			CountryCode: strings.ToUpper(category),
			Domain:      domainEntries,
		})
	}

	data, err := proto.Marshal(&geositeList)
	if err != nil {
		log.Fatalf("Ошибка сборки GeoSite Protobuf: %v", err)
	}
	os.WriteFile("geosite.dat", data, 0644)
	fmt.Println("geosite.dat успешно сгенерирован!")
}

// readDirectory читает все файлы в папке и добавляет их в общую мапу категорий
func readDirectory(dir string, categories map[string]map[string]struct{}, baseDir string) {
	files, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("Предупреждение: папка %s не прочитана: %v\n", dir, err)
		return
	}

	for _, file := range files {
		if file.IsDir() {
			continue // Игнорируем подпапки
		}

		category := file.Name()
		if categories[category] == nil {
			categories[category] = make(map[string]struct{})
		}

		readLocalDomains(baseDir, category, categories[category])
	}
}

// readLocalDomains читает конкретный файл и рекурсивно обрабатывает директивы include:
func readLocalDomains(baseDir, filename string, domains map[string]struct{}) {
	filePath := filepath.Join(baseDir, filename)
	file, err := os.Open(filePath)
	if err != nil {
		log.Printf("Предупреждение: не удалось открыть %s\n", filePath)
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "include:") {
			includeStr := strings.TrimPrefix(line, "include:")
			includeFile := strings.Fields(includeStr)[0]
			readLocalDomains(baseDir, includeFile, domains)
			continue
		}

		// Используем очистку для локальных файлов (без удаления префикса domain:)
		domain := cleanLocalDomain(line)
		if domain != "" {
			domains[domain] = struct{}{}
		}
	}
}

// cleanLocalDomain очищает строки из папок data и nash
func cleanLocalDomain(line string) string {
	domain := strings.Fields(line)[0]
	// ВАЖНО: по твоей просьбе мы НЕ убираем "domain:" в локальных файлах
	domain = strings.TrimPrefix(domain, "full:")
	return strings.ToLower(domain)
}

// cleanRemoteDomain очищает строки из файлов, скачанных по ссылкам в config.json
func cleanRemoteDomain(line string) string {
	domain := strings.Fields(line)[0]
	// Убираем префикс "domain:" только здесь
	domain = strings.TrimPrefix(domain, "domain:") 
	domain = strings.TrimPrefix(domain, "full:")
	return strings.ToLower(domain)
}

// fetchLines скачивает файл по ссылке и разбивает его на строки
func fetchLines(url string) []string {
	resp, err := http.Get(url)
	if err != nil {
		log.Printf("Ошибка скачивания %s: %v\n", url, err)
		return nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	scanner := bufio.NewScanner(bytes.NewReader(body))
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}