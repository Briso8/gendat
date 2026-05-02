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
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/v2fly/v2ray-core/v5/app/router/routercommon"
	"go4.org/netipx"
	"google.golang.org/protobuf/proto"
)

type Config struct {
	GeoIP map[string][]string `json:"geoip"`
}

func main() {
	mode := flag.String("mode", "", "Режим работы: geosite или geoip")
	dataDir := flag.String("data", "./external-domains/data", "Путь к клонированной папке data (для geosite)")
	flag.Parse()

	switch *mode {
	case "geosite":
		buildGeoSite(*dataDir)
	case "geoip":
		// Для GeoIP по-прежнему нужен config.json
		configBytes, err := os.ReadFile("config.json")
		if err != nil {
			log.Fatalf("Ошибка чтения config.json: %v", err)
		}
		var cfg Config
		if err := json.Unmarshal(configBytes, &cfg); err != nil {
			log.Fatalf("Ошибка парсинга config.json: %v", err)
		}
		buildGeoIP(cfg.GeoIP)
	default:
		log.Fatal("Укажите режим: -mode geosite или -mode geoip")
	}
}

// ---------------- GEOSITE (Локальная папка) ----------------

func buildGeoSite(dataDir string) {
	var geositeList routercommon.GeoSiteList

	files, err := os.ReadDir(dataDir)
	if err != nil {
		log.Fatalf("Ошибка чтения папки data (%s): %v", dataDir, err)
	}

	for _, file := range files {
		if file.IsDir() {
			continue // Игнорируем подпапки
		}

		category := file.Name() // Имя файла становится категорией
		domains := make(map[string]struct{})

		// Читаем домены рекурсивно (с поддержкой include:)
		readLocalDomains(dataDir, category, domains)

		var domainEntries[]*routercommon.Domain
		for d := range domains {
			domainEntries = append(domainEntries, &routercommon.Domain{
				Type:  routercommon.Domain_Domain,
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
	fmt.Println("geosite.dat успешно сгенерирован")
}

// readLocalDomains читает файл и обрабатывает директивы "include:"
func readLocalDomains(dir, filename string, domains map[string]struct{}) {
	filePath := filepath.Join(dir, filename)
	file, err := os.Open(filePath)
	if err != nil {
		log.Printf("Предупреждение: не удалось открыть %s (возможно, битая ссылка include)", filePath)
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Если строка содержит include:, рекурсивно читаем указанный файл
		if strings.HasPrefix(line, "include:") {
			includeFile := strings.TrimPrefix(line, "include:")
			readLocalDomains(dir, includeFile, domains)
			continue
		}

		// Дедупликация и добавление обычного домена
		domains[strings.ToLower(line)] = struct{}{}
	}
}

// ---------------- GEOIP (Ссылки из конфига) ----------------

func buildGeoIP(categories map[string][]string) {
	var geoipList routercommon.GeoIPList

	for category, urls := range categories {
		var cidrs []netip.Prefix
		var singles[]netip.Addr
		seen := make(map[string]struct{})

		for _, u := range urls {
			lines := fetchLines(u)
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				if _, ok := seen[line]; ok {
					continue
				}
				seen[line] = struct{}{}

				if strings.Contains(line, "/") {
					pref, err := netip.ParsePrefix(line)
					if err == nil && pref.Addr().Is4() {
						cidrs = append(cidrs, pref)
					}
				} else {
					ip, err := netip.ParseAddr(line)
					if err == nil && ip.Is4() {
						singles = append(singles, ip)
					}
				}
			}
		}

		var builder netipx.IPSetBuilder
		for _, c := range cidrs {
			builder.AddPrefix(c)
		}
		ipSet, _ := builder.IPSet()

		var finalCIDRs[]*routercommon.CIDR

		// Добавляем неизменные подсети
		for _, c := range cidrs {
			ipBytes := c.Addr().As4()
			finalCIDRs = append(finalCIDRs, &routercommon.CIDR{
				Ip:     ipBytes[:],
				Prefix: uint32(c.Bits()),
			})
		}

		// Добавляем одиночные IP, если они НЕ входят в подсети
		for _, ip := range singles {
			if !ipSet.Contains(ip) {
				ipBytes := ip.As4()
				finalCIDRs = append(finalCIDRs, &routercommon.CIDR{
					Ip:     ipBytes[:],
					Prefix: 32,
				})
			}
		}

		geoipList.Entry = append(geoipList.Entry, &routercommon.GeoIP{
			CountryCode: strings.ToUpper(category),
			Cidr:        finalCIDRs,
		})
	}

	data, err := proto.Marshal(&geoipList)
	if err != nil {
		log.Fatalf("Ошибка сборки GeoIP Protobuf: %v", err)
	}
	os.WriteFile("geoip.dat", data, 0644)
	fmt.Println("geoip.dat успешно сгенерирован")
}

func fetchLines(url string)[]string {
	resp, err := http.Get(url)
	if err != nil {
		log.Printf("Ошибка скачивания %s: %v\n", url, err)
		return nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	scanner := bufio.NewScanner(bytes.NewReader(body))
	var lines[]string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}
