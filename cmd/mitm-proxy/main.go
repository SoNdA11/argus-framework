package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"argus-framework/internal/web"
	"argus-framework/pkg/ble"
	"argus-framework/pkg/config"
)

func main() {
	fmt.Println("Iniciando Argus Framework - MitM Proxy...")

	// 1. Carrega as configurações do arquivo config.json.
	cfg, err := config.Load("configs/config.json")
	if err != nil {
		log.Fatalf("❌ Erro ao carregar configs/config.json: %v", err)
	}

	// 2. Cria um 'context' que pode ser cancelado.
	// Quando o usuário aperta Ctrl+C, a função 'cancel()' é chamada.
	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Println("\nSinal de interrupção recebido, encerrando...")
		cancel()
	}()

	// 3. Inicializa as estruturas de estado e canais de comunicação.
	var wg sync.WaitGroup	// Usado para garantir que a função 'main' espere todas as goroutines terminarem.
	commandChannel := make(chan []byte, 10)	// Canal para passar comandos de resistência do Servidor para o Cliente.

	// Cria as instâncias das estruturas de estado que serão compartilhadas (via ponteiros) entre as goroutines.
	uiState := &ble.UIState{}
	powerCfg := &ble.AttackConfig{Active: true, Mode: "aditivo", Value: 50}
	resistanceCfg := &ble.ResistanceConfig{Forward: true}

	// 4. Inicia as três goroutines principais, que rodarão em paralelo.
	wg.Add(3)	// Informa ao WaitGroup que estamos esperando 3 goroutines.
	
	go ble.ClientRoutine(ctx, cfg, commandChannel, uiState, resistanceCfg, &wg)
	go ble.ServerRoutine(ctx, cfg, commandChannel, uiState, powerCfg, &wg)
	go web.HubRoutine(ctx, cancel, uiState, powerCfg, resistanceCfg, &wg)

	fmt.Println("✅ Aplicação rodando. Acesse http://localhost:8080 para ver o dashboard.")

	// 5. Bloqueia a execução da 'main' até que as 3 goroutines chamem 'wg.Done()'.
	wg.Wait()
	fmt.Println("Aplicação encerrada.")
}