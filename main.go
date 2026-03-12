package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gocolly/colly"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Curso struct {
	URL     string
	Titulo  string
	Lugar   string
	Periodo string
	Hora    string
	Plazas  string
	Costo   string
}

var (
	telegramBotToken string
	telegramChatID   string
	telegramThreadID string
	databaseURL      string
)

func initDB() *pgxpool.Pool {
	if databaseURL == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		log.Fatal("Failed to parse DATABASE_URL:", err)
	}

	config.MaxConns = 5
	config.MinConns = 1
	config.MaxConnLifetime = time.Hour
	config.MaxConnIdleTime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		log.Fatal("Failed to create connection pool:", err)
	}

	if err := pool.Ping(context.Background()); err != nil {
		log.Fatal("Failed to ping database:", err)
	}

	log.Println("Connected to PostgreSQL successfully")
	return pool
}

func runMigrations() error {
	if databaseURL == "" {
		return fmt.Errorf("DATABASE_URL not set")
	}

	m, err := migrate.New(
		"file://migrations",
		databaseURL,
	)
	if err != nil {
		return fmt.Errorf("failed to create migrate instance: %w", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	log.Println("Migrations completed successfully")
	return nil
}

func cursoExiste(ctx context.Context, pool *pgxpool.Pool, url string) bool {
	var exists bool
	err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM cursos WHERE url=$1)", url).Scan(&exists)
	if err != nil {
		log.Println("Error checking if curso exists:", err)
		return false
	}
	return exists
}

func guardarCurso(ctx context.Context, pool *pgxpool.Pool, curso Curso) {
	_, err := pool.Exec(ctx,
		"INSERT INTO cursos (url, titulo, lugar, periodo, hora, plazas, costo) VALUES ($1, $2, $3, $4, $5, $6, $7)",
		curso.URL, curso.Titulo, curso.Lugar, curso.Periodo, curso.Hora, curso.Plazas, curso.Costo)
	if err != nil {
		log.Println("Error guardando en la base de datos:", err)
	}
}

func sendTelegramMessage(message string) {
	telegramAPI := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", telegramBotToken)
	params := url.Values{}
	params.Set("chat_id", telegramChatID)
	params.Set("text", message)
	params.Set("parse_mode", "Markdown")
	if telegramThreadID != "" {
		fmt.Println("threadId:", telegramThreadID)
		params.Set("message_thread_id", telegramThreadID)
	}

	_, err := http.PostForm(telegramAPI, params)
	if err != nil {
		log.Println("Error enviando mensaje a Telegram:", err)
	}
}

func setupGracefulShutdown(pool *pgxpool.Pool) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	go func() {
		sig := <-c
		log.Printf("Received signal %v, shutting down gracefully...", sig)
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		cancel()
		go func() {
			<-shutdownCtx.Done()
			pool.Close()
		}()
	}()

	return ctx
}

func main() {
	var runMigrationsFlag bool

	flag.StringVar(&databaseURL, "database-url", "", "Database connection URL (env: DATABASE_URL)")
	flag.StringVar(&telegramBotToken, "token", "", "Token del bot de Telegram (env: TELEGRAM_TOKEN)")
	flag.StringVar(&telegramChatID, "chatid", "", "ID del chat de Telegram (env: TELEGRAM_CHATID)")
	flag.StringVar(&telegramThreadID, "threadid", "", "ID del hilo en Telegram (env: TELEGRAM_THREADID)")
	flag.BoolVar(&runMigrationsFlag, "migrate", false, "Run database migrations before starting")
	flag.Parse()

	if databaseURL == "" {
		databaseURL = os.Getenv("DATABASE_URL")
	}
	if telegramBotToken == "" {
		telegramBotToken = os.Getenv("TELEGRAM_TOKEN")
	}
	if telegramChatID == "" {
		telegramChatID = os.Getenv("TELEGRAM_CHATID")
	}
	if telegramThreadID == "" {
		telegramThreadID = os.Getenv("TELEGRAM_THREADID")
	}

	if databaseURL == "" {
		log.Fatal("Se requiere la URL de la base de datos. Usa el flag -database-url o la variable DATABASE_URL")
	}
	if telegramBotToken == "" {
		log.Fatal("Se requiere el token de Telegram. Usa el flag -token o la variable TELEGRAM_TOKEN")
	}
	if telegramChatID == "" {
		log.Fatal("Se requiere el ID del chat. Usa el flag -chatid o la variable TELEGRAM_CHATID")
	}

	if runMigrationsFlag {
		if err := runMigrations(); err != nil {
			log.Fatal("Failed to run migrations:", err)
		}
	}

	pool := initDB()
	defer pool.Close()

	ctx := setupGracefulShutdown(pool)

	c := colly.NewCollector(
		colly.AllowedDomains("formacionagraria.tenerife.es"),
		colly.Async(true),
	)
	c.Limit(&colly.LimitRule{
		DomainGlob:  "*",
		Parallelism: 3,
		Delay:       1 * time.Second,
	})

	var cursos []Curso

	c.OnHTML("a[href^='/acfor-fo/actividades/']", func(e *colly.HTMLElement) {
		link := e.Attr("href")
		if strings.Contains(link, "solicitud") {
			return
		}
		if !strings.HasPrefix(link, "http") {
			link = e.Request.AbsoluteURL(link)
		}
		e.Request.Visit(link)
	})

	c.OnHTML("div.container.page-body", func(e *colly.HTMLElement) {
		titulo := e.ChildText("span.convocatoria-titulo")
		if titulo == "" {
			return
		}
		curso := Curso{
			URL:    e.Request.URL.String(),
			Titulo: titulo,
		}
		e.ForEach("div.row", func(_ int, row *colly.HTMLElement) {
			label := row.ChildText("label")
			value := row.ChildText("div.col-xs-6.col-sm-8.col-md-10 span")
			switch strings.TrimSpace(label) {
			case "Lugar de impartición:":
				curso.Lugar = value
			case "Período de impartición:":
				curso.Periodo = value
			case "Horario de impartición:":
				curso.Hora = value
			case "Plazas disponibles:":
				curso.Plazas = value
			case "Importe:":
				curso.Costo = value
			}
		})
		if !cursoExiste(ctx, pool, curso.URL) {
			cursos = append(cursos, curso)
			guardarCurso(ctx, pool, curso)
		}
	})

	c.OnRequest(func(r *colly.Request) {
		fmt.Println("Visitando:", r.URL)
	})

	c.OnError(func(_ *colly.Response, err error) {
		log.Println("Error:", err)
	})

	if err := c.Visit("https://formacionagraria.tenerife.es/"); err != nil {
		log.Fatal(err)
	}
	c.Wait()

	var messageBuilder strings.Builder
	if len(cursos) > 0 {
		messageBuilder.WriteString("¡Hay cursos nuevos!\n\n")
		for i, curso := range cursos {
			fmt.Println("Preparando mensaje para curso:", curso.Titulo)
			messageBuilder.WriteString(
				fmt.Sprintf("*Curso %d:*\nTítulo: %s\nLugar: %s\nPeríodo: %s\nHorario: %s\nPlazas: %s\nCosto: %s\n[Ver más](%s)\n\n",
					i+1, curso.Titulo, curso.Lugar, curso.Periodo, curso.Hora, curso.Plazas, curso.Costo, curso.URL))
		}
		sendTelegramMessage(messageBuilder.String())
		fmt.Println("Mensaje enviado a:", telegramChatID)
	}
}
