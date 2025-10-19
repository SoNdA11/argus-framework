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
	"github.com/go-ble/ble/linux"
	blelib "github.com/go-ble/ble"
)

// Constantes adicionadas para CSC
var (
	cscServiceUUID         = blelib.MustParse("00001816-0000-1000-8000-00805f9b34fb")
	cscMeasurementCharUUID = blelib.MustParse("00002a5b-0000-1000-8000-00805f9b34fb")
)

// Função auxiliar para parsear dados de CSC Measurement
// Retorna (cadência, erro)
func parseCSMCadence(data []byte) (int, error) {
	if len(data) < 1 {
		return 0, fmt.Errorf("dados CSC muito curtos")
	}
	flags := data[0]
	offset := 1 // Começa a ler após as flags

	// Ignora dados de Wheel Revolution se presentes (bit 0)
	if flags&0x01 != 0 {
		if len(data) < offset+6 { // 4 bytes para revs, 2 para time
			return 0, fmt.Errorf("dados CSC incompletos (wheel)")
		}
		offset += 6
	}

	// Verifica se dados de Crank Revolution estão presentes (bit 1)
	if flags&0x02 != 0 {
		if len(data) < offset+4 { // 2 bytes para revs, 2 para time
			return 0, fmt.Errorf("dados CSC incompletos (crank)")
		}
		// Precisamos de pelo menos dois pacotes para calcular RPM
		// Esta função SÓ PARSEIA, não armazena estado.
		// A lógica de cálculo de RPM precisa ser feita no handler
		// comparando este pacote com o anterior.
		// Por simplicidade AGORA, vamos retornar 0. O ideal seria
		// armazenar o estado (lastRevs, lastTime) no ClientRoutine.
		
		// Placeholder - Implementação Simplificada: Apenas lê os bytes, não calcula RPM real
		// crankRevolutions := binary.LittleEndian.Uint16(data[offset : offset+2])
		// lastCrankEventTime := binary.LittleEndian.Uint16(data[offset+2 : offset+4])
		// log.Printf("[DEBUG CSC] Crank Revs: %d, Time: %d", crankRevolutions, lastCrankEventTime) // Debug
		
		// Retorna um valor fixo ou 0 por enquanto, pois o cálculo correto é complexo aqui
		// return 90, nil // Exemplo: Retorna 90 RPM se dados de crank existem

		// TODO: Implementar cálculo real de RPM baseado em dois pacotes CSC consecutivos
		return -1, fmt.Errorf("cálculo de RPM de CSC ainda não implementado") // Sinaliza que precisa implementar

	}

	return 0, fmt.Errorf("pacote CSC não contém dados de Crank Revolution")
}

func ClientRoutine(ctx context.Context, cfg *config.AppConfig, commandChan <-chan []byte, uiState *UIState, resistanceCfg *ResistanceConfig, wg *sync.WaitGroup) {
	defer wg.Done()
	log.Println("[CLIENTE] Goroutine do cliente iniciada.")

	// --- Variáveis para cálculo de cadência ---
	var lastCrankRevs uint16 = 0
	var lastCrankTime uint16 = 0 // Em 1/1024 segundos
	var cadenceInitialized bool = false
	// --- Fim Variáveis cadência ---


	for { // Loop principal de reconexão
		if ctx.Err() != nil { log.Println("[CLIENTE] Encerrando rotina cliente."); return }

		log.Printf("[CLIENTE] Selecionando adaptador hci%d...", cfg.ClientAdapterID)
		d, err := linux.NewDevice(blelib.OptDeviceID(cfg.ClientAdapterID))
		if err != nil { log.Printf("[CLIENTE] ❌ Falha adaptador: %s. Tentando em 5s.", err); time.Sleep(5 * time.Second); continue }
		blelib.SetDefaultDevice(d)

		log.Printf("[CLIENTE] 📡 Procurando pelo rolo %s...", cfg.TrainerMAC)
		advFilter := func(a blelib.Advertisement) bool { return strings.EqualFold(a.Addr().String(), cfg.TrainerMAC) }
		connectCtx, cancelConnect := context.WithTimeout(ctx, 15*time.Second)
		client, err := blelib.Connect(connectCtx, advFilter)
		cancelConnect()

		if err != nil { log.Printf("[CLIENTE] Falha conectar: %v. Tentando novamente.", err); d.Stop(); time.Sleep(5 * time.Second); continue }

		log.Println("[CLIENTE] ✅ Conectado ao rolo real!")
		uiState.Lock(); uiState.ClientConnected = true; uiState.Unlock() // Atualiza estado
		disconnectedChan := client.Disconnected()

		log.Println("[CLIENTE] Aguardando 1s e descobrindo perfil completo...")
		time.Sleep(1 * time.Second)
		profile, err := client.DiscoverProfile(true)
		if err != nil { log.Printf("[CLIENTE] ❌ Falha descobrir perfil: %v", err); client.CancelConnection(); <-disconnectedChan; log.Println("[CLIENTE] Desconexão confirmada (falha perfil)."); uiState.Lock(); uiState.ClientConnected = false; uiState.Unlock(); d.Stop(); continue }

		// --- Procura Potência ---
		powerChar := FindCharacteristic(profile, PowerCharUUIDStr) // Usando UUID string do shared.go
		if powerChar == nil { log.Println("[CLIENTE] ❌ Característica potência (2A63) não encontrada."); client.CancelConnection(); <-disconnectedChan; log.Println("[CLIENTE] Desconexão confirmada (char potência)."); uiState.Lock(); uiState.ClientConnected = false; uiState.Unlock(); d.Stop(); continue }
		log.Println("[CLIENTE] ✅ Característica Potência (2A63) encontrada!")

		// --- Procura Cadência ---
		cscChar := FindCharacteristic(profile, cscMeasurementCharUUID.String()) // Usando UUID convertido para string
		if cscChar == nil {
			log.Println("[CLIENTE] ⚠️ Característica Cadência (2A5B) não encontrada. Cadência real não será lida.")
		} else {
			log.Println("[CLIENTE] ✅ Característica Cadência (2A5B) encontrada!")
		}
		// --- Fim Procura Cadência ---

		// --- Procura Controle (Opcional, mas estava no seu código original) ---
		resistanceControlChar := FindCharacteristic(profile, "0000fff1-0000-1000-8000-00805f9b34fb") // Exemplo Wahoo Kickr?
		if resistanceControlChar == nil {
			log.Println("[CLIENTE] ⚠️ Característica de controle de resistência não encontrada.")
		}
		// --- Fim Procura Controle ---

		// --- Inscreve-se na Potência ---
		log.Println("[CLIENTE] 🔔 Inscrevendo-se para dados de potência real...")
		err = client.Subscribe(powerChar, false, func(data []byte) {
			if len(data) >= 4 {
				powerValue := binary.LittleEndian.Uint16(data[2:4])
				uiState.Lock()
				uiState.RealPower = int(powerValue)
				uiState.Unlock()
			}
		})
		if err != nil { log.Printf("[CLIENTE] ❌ Falha inscrever potência: %v", err); client.CancelConnection(); <-disconnectedChan; log.Println("[CLIENTE] Desconexão confirmada (falha sub potência)."); uiState.Lock(); uiState.ClientConnected = false; uiState.Unlock(); d.Stop(); continue }
		log.Println("[CLIENTE] Inscrição Potência OK.")

		// --- Inscreve-se na Cadência (se encontrada) ---
		if cscChar != nil {
			log.Println("[CLIENTE] 🔔 Inscrevendo-se para dados de cadência real...")
			err = client.Subscribe(cscChar, false, func(data []byte) {
				// --- LÓGICA DE CÁLCULO DE CADÊNCIA ---
				if len(data) < 1 { return }
				flags := data[0]
				offset := 1

				// Ignora Wheel data
				if flags&0x01 != 0 { if len(data) < offset+6 { return }; offset += 6 }

				// Processa Crank data
				if flags&0x02 != 0 {
					if len(data) < offset+4 { return }
					crankRevs := binary.LittleEndian.Uint16(data[offset : offset+2])
					crankTime := binary.LittleEndian.Uint16(data[offset+2 : offset+4]) // Em 1/1024 s

					if cadenceInitialized { // Só calcula se tivermos dados anteriores
						var revDiff uint16
						if crankRevs < lastCrankRevs { // Handle rollover (uint16)
							revDiff = (0xFFFF - lastCrankRevs) + crankRevs + 1
						} else {
							revDiff = crankRevs - lastCrankRevs
						}

						var timeDiff uint16
						if crankTime < lastCrankTime { // Handle rollover (uint16)
							timeDiff = (0xFFFF - lastCrankTime) + crankTime + 1
						} else {
							timeDiff = crankTime - lastCrankTime
						}

						if timeDiff > 0 && revDiff > 0 { // Evita divisão por zero e calcula apenas se houve mudança
							// Calcula RPM: (Revoluções / Tempo em segundos) * 60
							rpm := (float64(revDiff) / (float64(timeDiff) / 1024.0)) * 60.0
							uiState.Lock()
							uiState.RealCadence = int(rpm)
							uiState.Unlock()
							// log.Printf("[CAD REAL] RPM: %.1f (Revs: %d -> %d, Time: %d -> %d)", rpm, lastCrankRevs, crankRevs, lastCrankTime, crankTime) // Debug
						} else if timeDiff > 2048 { // Se passou mais de 2s sem pedalada, zera RPM
								uiState.Lock(); uiState.RealCadence = 0; uiState.Unlock()
						}
					}

					// Atualiza valores anteriores e marca como inicializado
					lastCrankRevs = crankRevs
					lastCrankTime = crankTime
					cadenceInitialized = true

				} else {
					// Pacote não tem dados de crank, talvez zerar a cadência?
					// cadenceInitialized = false // Reseta se parar de receber crank data?
					// uiState.Lock(); uiState.RealCadence = 0; uiState.Unlock()
				}
				// --- FIM LÓGICA CADÊNCIA ---
			})
			if err != nil { log.Printf("[CLIENTE] ❌ Falha inscrever cadência: %v", err); /* Não desconecta por isso */ } else { log.Println("[CLIENTE] Inscrição Cadência OK.") }
		}
		// --- Fim Inscrição Cadência ---


		log.Println("[CLIENTE] Pronto. Monitorando conexão e recebendo comandos...")

		// Loop para receber comandos de resistência (se aplicável) e monitorar desconexão
	clientLoop:
		for {
			select {
			case command := <-commandChan: // Recebe comandos do servidor (atualmente dummy)
				if resistanceCfg != nil && resistanceControlChar != nil {
					resistanceCfg.RLock()
					shouldForward := resistanceCfg.Forward
					resistanceCfg.RUnlock()
					if shouldForward {
						log.Printf("[CLIENTE] >> Encaminhando comando resistência: 0x%s\n", hex.EncodeToString(command))
						if err := client.WriteCharacteristic(resistanceControlChar, command, false); err != nil {
							log.Printf("[CLIENTE] Erro encaminhar comando: %s\n", err)
						}
					} else {
						log.Println("[CLIENTE] -- Comando resistência bloqueado.")
					}
				}
			case <-disconnectedChan:
				log.Println("[CLIENTE] 🔌 Desconectado do rolo real. Tentando reconectar...")
				uiState.Lock(); uiState.ClientConnected = false; uiState.RealPower = -1; uiState.RealCadence = -1 ; uiState.Unlock() // Reseta estado
				cadenceInitialized = false // Reseta cálculo de cadência
				break clientLoop // Sai do loop interno para reconectar
			case <-ctx.Done():
				log.Println("[CLIENTE] Contexto cancelado. Desconectando..."); client.CancelConnection(); <-disconnectedChan; log.Println("[CLIENTE] Desconexão confirmada (cancelamento).")
				uiState.Lock(); uiState.ClientConnected = false; uiState.RealPower = -1; uiState.RealCadence = -1; uiState.Unlock()
				d.Stop()
				return // Sai da função ClientRoutine
			}
		}
		d.Stop() // Libera o dispositivo antes da próxima iteração do loop for
	}
}