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

	"github.com/go-ble/ble/linux"
	goble "github.com/go-ble/ble"

)

// discoverAdapters procura por adaptadores BLE funcionais no sistema.
// Retorna os IDs dos dois primeiros adaptadores que encontrar.
func discoverAdapters() (int, int, error) {
	log.Println("[DISCOVERY] Procurando por adaptadores BLE disponíveis...")
	var availableIDs []int

	// Tenta de hci0 a hci9
	for i := 0; i < 10; i++ {
		// Tenta inicializar o dispositivo. Este é o teste crucial.
		// É a mesma função usada pelas Client/Server routines.
		d, err := linux.NewDevice(goble.OptDeviceID(i)) 
		if err != nil {
			// Se falhar (ex: "no such device" ou "operation not possible due to RF-kill"),
			// apenas registra e continua para o próximo.
			log.Printf("[DISCOVERY] Adaptador hci%d indisponível. Ignorando. (Erro: %v)", i, err)
			continue
		}

		// Precisamos fechar o dispositivo para liberar o recurso
		// para as outras goroutines.
		if err := d.Stop(); err != nil {
			log.Printf("[DISCOVERY] Aviso: falha ao fechar o adaptador hci%d após o teste: %v", i, err)
		}

		// Se chegamos aqui, o adaptador existe e não está em RF-kill.
		log.Printf("[DISCOVERY] ✅ Adaptador hci%d encontrado e disponível.", i)
		availableIDs = append(availableIDs, i)

		// Se já temos 2, podemos parar de procurar.
		if len(availableIDs) == 2 {
			break
		}
	}

	// Verifica se encontramos o suficiente.
	if len(availableIDs) < 2 {
		return 0, 0, fmt.Errorf("❌ FALHA NA DESCOBERTA: Não foi possível encontrar 2 adaptadores BLE disponíveis.\n   Verifique se ambos estão conectados e se o Bluetooth não está desligado (RF-kill)")
	}

	// Retorna o primeiro e o segundo adaptadores encontrados.
	clientID := availableIDs[0]
	serverID := availableIDs[1]
	log.Printf("[DISCOVERY] Usando hci%d como CLIENTE e hci%d como SERVIDOR.", clientID, serverID)

	return clientID, serverID, nil
}

func main() {
	fmt.Println("Iniciando Argus Framework - MitM Proxy...")

	// 1. Carrega as configurações do arquivo config.json.
	cfg, err := config.Load("configs/config.json")
	if err != nil {
		log.Fatalf("❌ Erro ao carregar configs/config.json: %v", err)
	}

	// 2. Chama a nova função de descoberta de adaptadores.
	clientID, serverID, err := discoverAdapters()
	if err != nil {
		// Se a descoberta falhar, o programa termina aqui com a mensagem de erro.
		log.Fatalf("❌ %v", err)
	}

	// 3. Sobrescreve os IDs do config com os que foram descobertos.
	// As routines de Cliente e Servidor agora usarão estes IDs dinâmicos.
	cfg.ClientAdapterID = clientID
	cfg.ServerAdapterID = serverID

	// 4. Cria um 'context' que pode ser cancelado.
	// Quando o usuário aperta Ctrl+C, a função 'cancel()' é chamada.
	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Println("\nSinal de interrupção recebido, encerrando...")
		cancel()
	}()

	// 5. Inicializa as estruturas de estado e canais de comunicação.
	var wg sync.WaitGroup	// Usado para garantir que a função 'main' espere todas as goroutines terminarem.
	commandChannel := make(chan []byte, 10)	// Canal para passar comandos de resistência do Servidor para o Cliente.

	// Cria as instâncias das estruturas de estado que serão compartilhadas (via ponteiros) entre as goroutines.
	uiState := &ble.UIState{}
	powerCfg := &ble.AttackConfig{Active: true, Mode: "aditivo", Value: 50}
	resistanceCfg := &ble.ResistanceConfig{Forward: true}

	// 6. Inicia as três goroutines principais, que rodarão em paralelo.
	wg.Add(3)	// Informa ao WaitGroup que estamos esperando 3 goroutines.
	
	go ble.ClientRoutine(ctx, cfg, commandChannel, uiState, resistanceCfg, &wg)
	go ble.ServerRoutine(ctx, cfg, commandChannel, uiState, powerCfg, &wg)
	go web.HubRoutine(ctx, cancel, uiState, powerCfg, resistanceCfg, &wg)

	fmt.Println("✅ Aplicação rodando. Acesse http://localhost:8080 para ver o dashboard.")

	// 7. Bloqueia a execução da 'main' até que as 3 goroutines chamem 'wg.Done()'.
	wg.Wait()
	fmt.Println("Aplicação encerrada.")
}