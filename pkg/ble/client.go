package ble

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"argus-framework/pkg/config"
	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux"
)

// ClientRoutine gerencia a conexão com o rolo de treino físico.
func ClientRoutine(ctx context.Context, cfg *config.AppConfig, commandChan <-chan []byte, uiState *UIState, resistanceCfg *ResistanceConfig, wg *sync.WaitGroup) {

	// Garante que o WaitGroup seja notificado quando a goroutine terminar.
	defer wg.Done()
	fmt.Println("[CLIENTE] Goroutine do cliente iniciada.")

	// Configura qual adaptador Bluetooth físico usar (ex: hci1).
	d, err := linux.NewDevice(ble.OptDeviceID(cfg.ClientAdapterID))
	if err != nil {
		log.Printf("[CLIENTE] ❌ Falha ao selecionar adaptador hci%d: %s", cfg.ClientAdapterID, err)
		return	// Encerra a goroutine se o adaptador não for encontrado.
	}
	ble.SetDefaultDevice(d)

	// Loop infinito para garantir que o programa sempre tente se reconectar se a conexão cair.
	for {

		// Se o programa estiver sendo encerrado (Ctrl+C), sai do loop.
		if ctx.Err() != nil {
			return
		}
		fmt.Printf("[CLIENTE] Procurando pelo rolo %s via hci%d...\n", cfg.TrainerMAC, cfg.ClientAdapterID)

		// Tenta se conectar a um dispositivo que tenha o MAC address especificado.
		client, err := ble.Connect(ctx, func(a ble.Advertisement) bool {
			return strings.EqualFold(a.Addr().String(), cfg.TrainerMAC)
		})
		if err != nil {
			time.Sleep(5 * time.Second)	// Se falhar, espera 5s e tenta de novo.
			continue
		}

		fmt.Println("[CLIENTE] ✅ Conectado ao rolo real!")

		// Atualiza o estado compartilhado para informar a UI.
		uiState.Lock()
		uiState.ClientConnected = true
		uiState.Unlock()

		// Canal que será sinalizado pela biblioteca quando a conexão for perdida.
		disconnectedChan := client.Disconnected()
		time.Sleep(1 * time.Second)

		// Descobre todos os serviços e características do rolo.
		profile, err := client.DiscoverProfile(true)
		if err != nil {
			client.CancelConnection()
			continue
		}

		powerChar := FindCharacteristic(profile, PowerCharUUIDStr)
		resistanceControlChar := FindCharacteristic(profile, "0000fff1-0000-1000-8000-00805f9b34fb")
		if powerChar == nil {
			log.Println("[CLIENTE] ❌ Característica de potência não encontrada.")
			client.CancelConnection()
			continue
		}

		powerHandler := func(data []byte) {
			if len(data) >= 4 {
				powerValue := binary.LittleEndian.Uint16(data[2:4])
				uiState.Lock()
				uiState.RealPower = int(powerValue)
				uiState.Unlock()
			}
		}
		if err := client.Subscribe(powerChar, false, powerHandler); err != nil {
			log.Printf("[CLIENTE] ❌ Falha ao se inscrever na potência: %s", err)
			client.CancelConnection()
			continue
		}
		fmt.Println("[CLIENTE] 🔔 Lendo dados de potência do rolo...")

	clientLoop:
		for {
			select {
			case <-disconnectedChan:

				uiState.Lock()
				uiState.ClientConnected = false
				uiState.Unlock()
				fmt.Println("[CLIENTE] 🔌 Desconectado do rolo. Tentando reconectar...")
				break clientLoop

			case command := <-commandChan:
				
				resistanceCfg.RLock()
				shouldForward := resistanceCfg.Forward
				resistanceCfg.RUnlock()

				if shouldForward && resistanceControlChar != nil {
					fmt.Printf("[CLIENTE] >> Encaminhando comando de resistência: 0x%s\n", hex.EncodeToString(command))
					if err := client.WriteCharacteristic(resistanceControlChar, command, false); err != nil {
						fmt.Printf("[CLIENTE] Erro ao encaminhar comando: %s\n", err)
					}
				}	else if resistanceControlChar != nil {
					fmt.Println("[CLIENTE] -- Comando de resistência recebido, mas bloqueado pela configuração.")
				}

			case <-ctx.Done():
				fmt.Println("[CLIENTE] Encerrando a goroutine do cliente.")
				client.CancelConnection()
				return
			}
		}
	}
}