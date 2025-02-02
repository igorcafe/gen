package main

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	urlpkg "net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	_ "modernc.org/sqlite"
)

type BookInfo struct {
	ID        string
	Authors   string
	Title     string
	Year      int
	Hash      string
	Extension string
	Language  string
	SizeStr   string
}

func main() {
	dir, err := os.UserConfigDir()
	if err != nil {
		log.Fatal(err)
	}

	db, err := sql.Open("sqlite", filepath.Join(dir, "gen.sqlite3"))
	if err != nil {
		log.Fatal(err)
	}

	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS cache (
		key TEXT PRIMARY KEY,
		value BLOB,
		timestamp TEXT
	)`)
	if err != nil {
		log.Fatal(err)
	}

	client := http.Client{}

	var extension string
	var author string
	var lang string
	flag.StringVar(&extension, "e", "", "filter by extension format")
	flag.StringVar(&author, "a", "", "filter by author")
	flag.StringVar(&lang, "l", "", "filter by language")
	flag.Parse()

	// fmt.Printf("-e '%s' -a '%s' | %v\n", extension, author, flag.Args())

	infos := []BookInfo{}
	docNumber := 1
	fmt.Printf("%s  %s  %s  %s  %s  %s\n", paddedText("TITLE", 70), paddedText("AUTHOR", 25), "YEAR", "EXT ", "LNG", "SIZE   ")
	// fmt.Println(strings.Repeat("-", 70 + 2 + 25 + 2 + 4 + 2 + 4 + 2 + 3 + 2 + 6))

	for page := 1; ; page++ {
		if page > 30 {
			// fmt.Println()
			// fmt.Println("Search interrupted after reaching page", page)
			break
		}
		baseURL := "https://libgen.is"
		url := baseURL + "/search.php"
		q := urlpkg.Values{}
		q.Set("res", "100")
		q.Set("page", fmt.Sprint(page))

		if author != "" {
			q.Set("column", "author")
			q.Set("req", author)
		} else {
			q.Set("column", "title")
			q.Set("req", strings.Join(flag.Args(), " "))
		}

		url += "?" + q.Encode()
		// fmt.Println(url)
		body := cacheGet(db, url, 24*time.Hour)
		if len(body) == 0 {
			resp, err := client.Get(url)
			if err != nil {
				log.Print(err)
				continue
			}
			body, err = io.ReadAll(resp.Body)
			if err != nil {
				log.Print(err)
				continue
			}
			cacheSet(db, url, body)
		}

		reader := bytes.NewReader(body)
		doc, _ := goquery.NewDocumentFromReader(reader)

		found := false

		doc.Find("table.c tr").Each(func(i int, s *goquery.Selection) {
			if i == 0 {
				return
			}

			found = true

			info := BookInfo{}

			s.Find("td").Each(func(i int, s *goquery.Selection) {
				col := strings.TrimSpace(s.Text())
				switch i {
				case 0:
					info.ID = col
				case 1:
					info.Authors = regexp.MustCompile(`\(.*?\)`).ReplaceAllString(col, "")
				case 2:
					bookURL := s.Find("a").Last().AttrOr("href", "")
					chunks := strings.Split(bookURL, "?")
					if len(chunks) > 1 {
						q, _ := urlpkg.ParseQuery(chunks[len(chunks)-1])
						info.Hash = strings.TrimSpace(q.Get("md5"))
					}
					// log.Printf("hash not found: %s", bookURL)
					extra := strings.TrimSpace(s.Find("i").Text())
					info.Title = strings.TrimRight(col, extra)
					info.Title = strings.TrimSpace(info.Title)
				case 4:
					info.Year, _ = strconv.Atoi(col)
				case 6:
					info.Language = strings.ToLower(col)
				case 7:
					info.SizeStr = strings.ToUpper(col)
				case 8:
					info.Extension = col
				}
			})

			if extension != "" && info.Extension != extension {
				return
			}

			if !fuzzyMatch(info.Title, strings.Join(flag.Args(), " ")) {
				return
			}

			if author != "" && !fuzzyMatch(info.Authors, author) {
				return
			}

			if lang != "" && len(lang) >= 3 && info.Language[:3] != lang[:3] {
				return
			}

			if info.Hash == "" {
				log.Printf("failed to get hash for book: %#v", info)
			}

			title := fmt.Sprintf("%d) %s", docNumber, info.Title)
			title = paddedText(title, 70)

			year := "unk."
			if info.Year != 0 {
				year = fmt.Sprint(info.Year)
			}

			ext := fmt.Sprintf("%s", info.Extension)
			ext = paddedText(ext, 4)

			authors := shortenAuthors(info.Authors)
			authors = paddedText(authors, 25)

			lang := info.Language[:3]

			size := info.SizeStr
			size = strings.Repeat(" ", max(0, 7-len(size))) + size

			fmt.Printf("%s  %s  %s  %s  %s  %s\n", title, authors, year, ext, lang, size)
			infos = append(infos, info)
			docNumber++
		})

		if !found {
			// fmt.Println()
			// fmt.Println("Search reached last page")
			break
		}
	}

	scanner := bufio.NewScanner(os.Stdin)

	var num int

	for {
		fmt.Print(": ")
		scanner.Scan()
		choice := scanner.Text()

		num, err = strconv.Atoi(choice)
		if err != nil {
			fmt.Println("type a valid number")
			continue
		}

		if num < 1 || num > len(infos) {
			fmt.Println("type a valid number")
			continue
		}

		break
	}

	info := infos[num-1]

	url := "https://books.ms/main/" + info.Hash
	body := cacheGet(db, url, 24*time.Hour)
	if len(body) == 0 {
		resp, err := http.Get(url)
		if err != nil {
			log.Fatal(err)
		}
		if resp.StatusCode != 200 {
			log.Fatalf("unexpected status code %d", resp.StatusCode)
		}
		body, err = io.ReadAll(resp.Body)
		if err != nil {
			log.Fatal(err)
		}
		cacheSet(db, url, body)
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		log.Fatal(err)
	}

	downloadDiv := doc.Find("#download").First()
	urlDirectDownload := downloadDiv.Find("h2 a").First().AttrOr("href", "")
	urlIPFSIO := downloadDiv.Find("ul li:nth-child(2) a").First().AttrOr("href", "")
	urlLocalIPFS := downloadDiv.Find("ul li:nth-child(4) a").First().AttrOr("href", "")

	fmt.Println()
	fmt.Println(doc.Find("#info > p:nth-child(4)").First().Text())
	fmt.Println(doc.Find("#info > p:nth-child(5)").First().Text())
	fmt.Println(doc.Find("#info > p:nth-child(6)").First().Text())
	fmt.Println(doc.Find("#info > p:nth-child(7)").First().Text())
	fmt.Println(url)
	fmt.Println()

	fmt.Println("1) Direct download")
	fmt.Println("2) Download from IPFS.io gateway")
	fmt.Println("3) Download from local IPFS gateway")

	for {
		fmt.Print(": ")
		scanner.Scan()
		num, err = strconv.Atoi(scanner.Text())
		if err != nil {
			fmt.Println("type a valid number")
			continue
		}
		if num < 1 || num > 3 {
			fmt.Println("type a valid number")
			continue
		}

		break
	}

	fmt.Println("Starting...")


	urls := []string{
		urlDirectDownload,
		urlIPFSIO,
		urlLocalIPFS,
	}

	url = urls[num-1]

	filename := fmt.Sprintf("%s - %s.%s", info.Authors, info.Title, info.Extension)
	tempFilename := ".partial." + filename
	file, err := os.Create(tempFilename)
	if err != nil {
		log.Fatal(err)
	}

	resp, err := http.Get(url)
	if err != nil {
		log.Fatal(err)
	}

	buf := make([]byte, 5*1024)

	elapsed := int64(0)
	total := resp.ContentLength

	start := time.Now()
	mavg := new(MAvg)
	mavg.Init(40)

	md5Hash := md5.New()
	teeBody := io.TeeReader(resp.Body, md5Hash)

	for {
		var n int
		n, err = teeBody.Read(buf)
		if n < 0 {
			err = io.ErrUnexpectedEOF
			break
		}
		if errors.Is(err, io.EOF) {
			err = nil
			if n == 0 {
				break
			}
		}
		if err != nil {
			break
		}
		_, err = file.Write(buf[:n])
		if err != nil {
			break
		}

		elapsed += int64(n)
		elapsedDur := time.Now().Sub(start)

		remaining := float64(total - elapsed)
		remainingDur := time.Duration(math.Ceil(float64(elapsedDur) * remaining / float64(elapsed)))

		mavg.Add(int(remainingDur))
		avgDur := time.Duration(mavg.CalcInt())
		avgDur /= time.Second
		avgDur *= time.Second

		perc := int(elapsed * 100 / total)
		perc = min(perc, 100)
		perc = max(0, perc)
		elapsedStr := strings.Repeat("=", perc/2)
		remainingStr := strings.Repeat("-", (100-perc)/2)
		fmt.Printf("[%s%s] (%.1f/%.1f K) [%s]      \r", elapsedStr, remainingStr, float64(elapsed)/1024, float64(total)/1024, avgDur.String())
	}

	fmt.Println()

	if err != nil {
		fmt.Printf("error during the download: %v\n", err)
		os.Exit(1)
	}

	err = file.Sync()
	if err != nil {
		fmt.Printf("error during the download: %v\n", err)
		os.Exit(1)
	}

	err = os.Rename(tempFilename, filename)
	if err != nil {
		fmt.Printf("error during the download: %v\n", err)
		os.Exit(1)
	}

	hashStr := hex.EncodeToString(md5Hash.Sum(nil))
	if strings.ToLower(info.Hash) != strings.ToLower(hashStr) {
		log.Printf("hash from downloaded file doesn't match libgen hash:\n%s (libgen) vs %s (local)\n", strings.ToLower(info.Hash), strings.ToLower(hashStr))
		os.Exit(1)
	}

	fmt.Printf("Successful downloaded file: %s\n", filename)
	// urlDirectDownload := fmt.Sprintf("https://download.books.ms/main/%s000/%s/%s\n", info.ID[:4], strings.ToLower(info.Hash), filename)
}

func paddedText(text string, length int) string {
	textLen := len([]rune(text))
	if textLen <= length {
		return text + strings.Repeat(" ", length-textLen)
	}
	textRunes := []rune(text)

	return string(textRunes[:length/2-3]) + "..." + string(textRunes[textLen-length/2:])
}

func fuzzyMatch(text string, query string) bool {
	opts := regexp.MustCompile(`\b+`).Split(query, -1)
	for _, opt := range opts {
		if opt == "" {
			continue
		}
		if !strings.Contains(strings.ToLower(text), strings.ToLower(opt)) {
			return false
		}
	}
	return true
}

func cacheGet(db *sql.DB, key string, ttl time.Duration) []byte {
	data := []byte{}
	var err error
	if ttl != 0 {
		row := db.QueryRow(`SELECT value FROM cache WHERE key = ? AND timestamp >= ?`, key, time.Now().Add(-ttl))
		err = row.Scan(&data)
	} else {
		row := db.QueryRow(`SELECT value FROM cache WHERE key = ?`, key)
		err = row.Scan(&data)
	}

	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}

	if err != nil {
		log.Print(err)
	}

	return data
}

func cacheSet(db *sql.DB, key string, value []byte) {
	_, err := db.Exec(`
		INSERT INTO cache (key, value, timestamp)
		VALUES (?1, ?2, ?3)
		ON CONFLICT DO UPDATE
		SET value = ?2, timestamp = ?3`,
		key, value, time.Now(),
	)

	if err != nil {
		log.Print(err)
	}
}

func shortenAuthors(authorsStr string) string {
	authorsStr = strings.TrimSpace(authorsStr)
	if authorsStr == "" {
		return ""
	}
	// log.Print("authorStr ", authorsStr)

	authors := []string{}
	if strings.Contains(authorsStr, ";") {
		authors = strings.Split(authorsStr, ";")
	} else {
		authors = strings.Split(authorsStr, ",")
	}

	res := ""
	for _, author := range authors {
		var name, surname string
		author = strings.TrimSpace(author)
		if author == "" {
			continue
		}
		// log.Print("author ", author)
		if strings.Contains(author, ",") {
			chunks := strings.Split(author, ",")
			surname = strings.TrimSpace(chunks[0])
			name = strings.TrimSpace(chunks[1])
			name = string([]rune(name)[0]) + "."
		} else {
			chunks := strings.Split(author, " ")
			surname = strings.TrimSpace(chunks[len(chunks)-1])
			name = strings.TrimSpace(chunks[0])
			name = string([]rune(name)[0]) + "."
		}

		if res == "" {
			res += name + " " + surname
		} else {
			res += "; " + name + " " + surname
		}
	}

	return res
}

type MAvg struct {
	data  []int
	count int
}

func (m *MAvg) Init(size int) {
	m.data = make([]int, size)
}

func (m *MAvg) Add(val int) {
	m.data[m.count%len(m.data)] = val
	m.count++
}

func (m *MAvg) Ready() bool {
	return m.count >= len(m.data)
}

func (m *MAvg) CalcInt() int {
	sum := 0
	filled := min(m.count, len(m.data))
	for i := 0; i < filled; i++ {
		sum += m.data[i]
	}

	return sum / filled
}
