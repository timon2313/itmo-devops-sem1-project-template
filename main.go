package main

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"github.com/joho/godotenv"

	_ "github.com/lib/pq"
)

type LoadedPrice struct {
	ID        string
	Createtdb time.Time
	Name      string
	Category  string
	Price     float64
}

type PostResponse struct {
	TotalItems      int     `json:"total_items"`
	TotalCategories int     `json:"total_categories"`
	TotalPrice      float64 `json:"total_price"`
}

func ConnectDB(dataSourceName string) (*sql.DB, error) {
	db, err := sql.Open("postgres", dataSourceName)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	_, err = db.Exec(`
        CREATE TABLE IF NOT EXISTS prices (
            id SERIAL PRIMARY KEY,
            created_at timestamp NOT NULL,
            name VARCHAR(255) NOT NULL,
            category VARCHAR(255) NOT NULL,
            price DECIMAL(10, 2) NOT NULL
        )
    `)
	fmt.Println("Connected to the database!")
	return db, err
}

func UploadPricesHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			log.Printf("Error retrieving file: %v", err)
			http.Error(w, "Failed to retrieve file", http.StatusBadRequest)
			return
		}
		defer file.Close()

		zipToBuffer := &bytes.Buffer{}
		if _, err := io.Copy(zipToBuffer, file); err != nil {
			log.Printf("Error reading file: %v", err)
			http.Error(w, "Failed to read file", http.StatusInternalServerError)
			return
		}

		zipReader, err := zip.NewReader(bytes.NewReader(zipToBuffer.Bytes()), int64(zipToBuffer.Len()))
		if err != nil {
			log.Printf("Error opening zip: %v", err)
			http.Error(w, "Invalid zip file", http.StatusBadRequest)
			return
		}

		var validRecords []LoadedPrice
		for _, zf := range zipReader.File {
			if filepath.Ext(zf.Name) != ".csv" {
				continue
			}
			csvFile, err := zf.Open()
			if err != nil {
				log.Printf("Error opening CSV in zip: %v", err)
				continue
			}
			defer csvFile.Close()

			reader := csv.NewReader(csvFile)
			if _, err := reader.Read(); err != nil {
				log.Printf("Error reading header: %v", err)
				continue
			}
			for {
				record, err := reader.Read()
				if err == io.EOF {
					break
				}
				if err != nil {
					log.Printf("Error reading CSV record: %v", err)
					continue
				}
				if len(record) < 5 {
					log.Printf("Invalid record: %v", record)
					continue
				}

				idStr := record[0]
				name := record[1]
				category := record[2]
				priceStr := record[3]
				dateStr := record[4]

				priceVal, err := strconv.ParseFloat(priceStr, 64)
				if err != nil {
					log.Printf("Invalid price: %v", priceStr)
					continue
				}

				createtdb, err := time.Parse("2024-01-01", dateStr)
				if err != nil {
					log.Printf("Invalid date: %v", dateStr)
					continue
				}

				validRecords = append(validRecords, LoadedPrice{
					ID:        idStr,
					Createtdb: createtdb,
					Name:      name,
					Category:  category,
					Price:     priceVal,
				})
			}
		}

		tr, err := db.Begin()
		if err != nil {
			log.Printf("Failed to begin transaction: %v", err)
			http.Error(w, "Failed to begin transaction", http.StatusInternalServerError)
			return
		}
		defer func() { _ = tr.Rollback() }()

		var completedRecieves int
		for _, rec := range validRecords {
			_, err := tr.Exec(`
                INSERT INTO prices (id, created_at, name, category, price)
                VALUES ($1, $2, $3, $4, $5)
                ON CONFLICT (id) DO NOTHING
            `, rec.ID, rec.Createtdb, rec.Name, rec.Category, rec.Price)
			if err != nil {
				log.Printf("DB insert error: %v", err)
				continue
			}
			completedRecieves++
		}

		var dbCategories int
		var dbTotalPrice float64
		row := tr.QueryRow(`
            SELECT COUNT(DISTINCT category), COALESCE(SUM(price), 0)
            FROM prices
        `)
		if err := row.Scan(&dbCategories, &dbTotalPrice); err != nil {
			log.Printf("Failed to scan totals: %v", err)
			http.Error(w, "Failed to calculate totals", http.StatusInternalServerError)
			return
		}

		if err := tr.Commit(); err != nil {
			log.Printf("Failed to commit transaction: %v", err)
			http.Error(w, "Failed to commit transaction", http.StatusInternalServerError)
			return
		}

		resp := PostResponse{
			TotalItems:      completedRecieves,
			TotalCategories: dbCategories,
			TotalPrice:      dbTotalPrice,
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.Printf("Error encoding JSON: %v", err)
		}
	}
}

func DownloadPricesHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rows, err := db.Query(`
            SELECT id, created_at, name, category, price 
            FROM prices
        `)
		if err != nil {
			log.Printf("Error querying database: %v", err)
			http.Error(w, "Failed to retrieve data", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var allPrices []LoadedPrice
		for rows.Next() {
			var (
				idInt     int
				createtdb time.Time
				name      string
				category  string
				priceVal  float64
			)
			if err := rows.Scan(&idInt, &createtdb, &name, &category, &priceVal); err != nil {
				log.Printf("Error scanning row: %v", err)
				continue
			}
			allPrices = append(allPrices, LoadedPrice{
				ID:        strconv.Itoa(idInt),
				Createtdb: createtdb,
				Name:      name,
				Category:  category,
				Price:     priceVal,
			})
		}
		if err := rows.Err(); err != nil {
			log.Printf("Error after rows.Next(): %v", err)
			http.Error(w, "Failed to read rows", http.StatusInternalServerError)
			return
		}

		csvBuffer := &bytes.Buffer{}
		writer := csv.NewWriter(csvBuffer)
		writer.Write([]string{"id", "name", "category", "price", "create_date"})
		for _, p := range allPrices {
			record := []string{
				p.ID,
				p.Name,
				p.Category,
				strconv.FormatFloat(p.Price, 'f', 2, 64),
				p.Createtdb.Format("2024-01-01"),
			}
			writer.Write(record)
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			log.Printf("Error finalizing CSV: %v", err)
			http.Error(w, "Failed to write CSV", http.StatusInternalServerError)
			return
		}

		zipToBuffer := &bytes.Buffer{}
		zipWriter := zip.NewWriter(zipToBuffer)
		csvFile, err := zipWriter.Create("data.csv")
		if err != nil {
			log.Printf("Error creating file in ZIP: %v", err)
			http.Error(w, "Failed to create ZIP", http.StatusInternalServerError)
			return
		}
		if _, err := csvFile.Write(csvBuffer.Bytes()); err != nil {
			log.Printf("Error writing CSV to ZIP: %v", err)
			http.Error(w, "Failed to write ZIP", http.StatusInternalServerError)
			return
		}
		if err := zipWriter.Close(); err != nil {
			log.Printf("Error closing ZIP writer: %v", err)
			http.Error(w, "Failed to close ZIP", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", "attachment; filename=data.zip")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(zipToBuffer.Bytes()); err != nil {
			log.Printf("Error sending ZIP file: %v", err)
		}
	}
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, loading environment variables")
	}
	dbHost := os.Getenv("POSTGRES_HOST")
	dbPort := os.Getenv("POSTGRES_PORT")
	dbUser := os.Getenv("POSTGRES_USER")
	dbPassword := os.Getenv("POSTGRES_PASSWORD")
	dbName := os.Getenv("POSTGRES_DB")
	pl := "postgres://" + dbUser + ":" + dbPassword + "@" + dbHost + ":" + dbPort + "/" + dbName + "?sslmode=disable"
	db, err := ConnectDB(pl)
	if err != nil {
		log.Fatalf("Failed to connect to the database: %v", err)
	}
	defer db.Close()
	router := mux.NewRouter()
	router.HandleFunc("/api/v0/prices", UploadPricesHandler(db)).Methods("POST")
	router.HandleFunc("/api/v0/prices", DownloadPricesHandler(db)).Methods("GET")
	log.Println("Server started on :8080")
	log.Fatal(http.ListenAndServe(":8080", router))
}
