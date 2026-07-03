package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/email"
	"payment-gateway/internal/logger"
	"payment-gateway/internal/server"
	"payment-gateway/internal/workers"
)

func main() {
	logger.Configure()
	log.Println("Iniciando o ecossistema concorrente em Go...")

	cfg := config.LoadConfig()

	db, err := database.ConnectPostgres(cfg)
	if err != nil {
		log.Fatalf("Erro fatal ao conectar no banco de dados: %v", err)
	}
	defer db.Close()

	// 1. Criamos um Contexto cancelável para gerenciar o desligamento ordenado da aplicação
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 2. Inicializa o gerenciador e dispara os Workers de Produção
	workerMgr := workers.NewWorkerManager(db, cfg)
	workerMgr.StartAll(ctx)

	mailer := email.NewService(cfg)
	api := server.New(cfg, db, workerMgr, mailer)
	httpServer := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      api.Handler(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("API HTTP iniciada na porta %s", cfg.Port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Erro fatal ao subir API HTTP: %v", err)
		}
	}()

	log.Println("API e motores em background foram disparados e isolados.")

	// 3. Captura sinais de desligamento do terminal (Ctrl+C, SIGTERM do Docker/Kubernetes)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	<-stop // O código "trava" aqui de forma eficiente até receber um comando de parada
	log.Println("Sinal de encerramento recebido. Desligando sistemas de forma limpa...")

	// Cancela o contexto principal, avisando a todas as Goroutines de background para pararem imediatamente
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Erro ao desligar HTTP server: %v", err)
	}
	log.Println("Aplicação finalizada com 100% de segurança de dados.")
}
