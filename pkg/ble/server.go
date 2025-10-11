package ble

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"
	"argus-framework/pkg/config"
	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux"
)

// ServerRoutine gerencia a simulação do rolo de treino virtual.
func ServerRoutine(ctx context.Context, cfg *config.AppConfig, commandChan chan<- []byte, uiState *UIState, powerCfg *AttackConfig, wg *sync.WaitGroup) {
	defer wg.Done()
	fmt.Println("[SERVIDOR] Goroutine do servidor iniciada.")
	d, err := linux.NewDevice(ble.OptDeviceID(cfg.ServerAdapterID))
	if err != nil {
		log.Fatalf("[SERVIDOR] ❌ Falha ao selecionar adaptador hci%d: %s", cfg.ServerAdapterID, err)
	}
	ble.SetDefaultDevice(d)

	// --- Definição de Serviços e Características ---
	powerSvc := ble.NewService(PowerSvcUUID)
	powerChar := powerSvc.NewCharacteristic(PowerCharUUID)

	cscSvc := ble.NewService(CSCSvcUUID)
	cscChar := cscSvc.NewCharacteristic(CSCMeasurementCharUUID)

	hrSvc := ble.NewService(HRSvcUUID)
	hrChar := hrSvc.NewCharacteristic(HRMeasurementCharUUID)

	ftmsSvc := ble.NewService(FTMSSvcUUID)
	ftmsFeatureChar := ftmsSvc.NewCharacteristic(FTMSFeatureCharUUID)
	ftmsControlPointChar := ftmsSvc.NewCharacteristic(FTMSControlPointCharUUID)
	

	deviceInfoSvc := ble.NewService(DeviceInfoSvcUUID)
	manufacturerNameChar := deviceInfoSvc.NewCharacteristic(ManufacturerNameCharUUID)
	modelNumberChar := deviceInfoSvc.NewCharacteristic(ModelNumberCharUUID)

	// --- Anexando os Handlers ---

	// Handler de Potência
	powerChar.HandleNotify(ble.NotifyHandlerFunc(func(req ble.Request, ntf ble.Notifier) {
		// (A lógica do handler de potência com ataque dinâmico e ruído permanece a mesma)
		log.Printf("[SERVIDOR] ✅ App %s inscrito (POTÊNCIA)", req.Conn().RemoteAddr())
        uiState.Lock()
        uiState.AppConnected = true
        uiState.Unlock()
        defer func() {
            uiState.Lock()
            uiState.AppConnected = false
            uiState.Unlock()
            log.Printf("[SERVIDOR] 🔌 App %s desinscrito (POTÊNCIA)", req.Conn().RemoteAddr())
        }()

        var currentBoostTarget int
        var nextChangeTime time.Time
        ticker := time.NewTicker(1 * time.Second)
        defer ticker.Stop()
        for {
            select {
			case <-ctx.Done():
				return
            case <-ntf.Context().Done():
                return
            case <-ticker.C:
                powerCfg.RLock()
                isActive, mode, vMin, vMax := powerCfg.Active, powerCfg.Mode, powerCfg.ValueMin, powerCfg.ValueMax
                powerCfg.RUnlock()
                uiState.RLock()
                realPower := uiState.RealPower
                uiState.RUnlock()
                
                if isActive && time.Now().After(nextChangeTime) {
                    if vMax > vMin { currentBoostTarget = rand.Intn(vMax-vMin+1) + vMin } else { currentBoostTarget = vMin }
                    randomInterval := rand.Intn(16) + 15 
                    nextChangeTime = time.Now().Add(time.Duration(randomInterval) * time.Second)
                    if mode == "aditivo" {
                        log.Printf("[SERVIDOR] Novo alvo de boost dinâmico: %dW (próxima mudança em %ds)", currentBoostTarget, randomInterval)
                    }
                }

                modifiedPower := realPower
                if isActive {
                    switch mode {
                    case "aditivo": modifiedPower = realPower + currentBoostTarget
                    case "percentual":
                        increase := float64(realPower) * (float64(vMin) / 100.0)
                        modifiedPower = realPower + int(increase)
                    }
                }

                potenciaFinal := modifiedPower
                if isActive && realPower > 0 {
                    ruido := rand.Intn(5) - 2
                    potenciaFinal = modifiedPower + ruido
                    if potenciaFinal < 0 { potenciaFinal = 0 }
                }
                
                uiState.Lock()
                uiState.ModifiedPower = potenciaFinal
                uiState.Unlock()

                powerBytes := make([]byte, 4)
                binary.LittleEndian.PutUint16(powerBytes[2:4], uint16(potenciaFinal))
                if _, err := ntf.Write(powerBytes); err != nil {
                    return
                }
            }
        }
	}))

	// Handler de Cadência.
	cscChar.HandleNotify(ble.NotifyHandlerFunc(func(req ble.Request, ntf ble.Notifier) {
		log.Printf("[SERVIDOR] ✅ App %s inscrito (CADÊNCIA)", req.Conn().RemoteAddr())
		defer log.Printf("[SERVIDOR] 🔌 App %s desinscrito (CADÊNCIA)", req.Conn().RemoteAddr())
		
		var cumulativeRevolutions uint16 = 0
		var lastCrankEventTime uint16 = 0
		var timeOfNextRevolution time.Time

		for {
			select {
			case <-ctx.Done():
				return
			case <-ntf.Context().Done():
				return
			// Usamos um ticker de alta frequência (20x por segundo) apenas para VERIFICAR.
			case <-time.After(50 * time.Millisecond):
				uiState.RLock()
				potenciaAtual := uiState.ModifiedPower
				uiState.RUnlock()

				simulatedCadence := 0
				if potenciaAtual > 0 {
					simulatedCadence = 85 + int(float64(potenciaAtual)/30.0) + rand.Intn(5) - 2
				}

				// Se não estamos pedalando, não fazemos nada.
				if simulatedCadence <= 0 {
					timeOfNextRevolution = time.Time{} // Reseta o tempo da próxima revolução
					continue
				}

				// Se o tempo da próxima revolução ainda não foi definido, define agora.
				if timeOfNextRevolution.IsZero() {
					timeOfNextRevolution = time.Now()
				}
				
				// Verificamos se já passou o tempo de registrar uma nova revolução.
				if time.Now().After(timeOfNextRevolution) {
					// SIM! Uma revolução aconteceu.
					cumulativeRevolutions++
					
					// O timestamp do evento é o tempo atual.
					lastCrankEventTime = uint16(time.Now().UnixNano() / 1e6 * 1024 / 1000)

					// Prepara o pacote de 5 bytes para enviar.
					flags := byte(0x02)
					buf := new(bytes.Buffer)
					binary.Write(buf, binary.LittleEndian, flags)
					binary.Write(buf, binary.LittleEndian, cumulativeRevolutions)
					binary.Write(buf, binary.LittleEndian, lastCrankEventTime)
					payload := buf.Bytes()

					log.Printf("[CADÊNCIA] Enviando Notificação! RPM: %d, Revs: %d, Payload: 0x%s",
						simulatedCadence, cumulativeRevolutions, hex.EncodeToString(payload))

					// Envia a notificação para o app.
					if _, err := ntf.Write(payload); err != nil {
						return // Sai se houver erro de escrita
					}
					
					// Calcula o tempo da PRÓXIMA revolução com base na cadência atual.
					intervalSeconds := 60.0 / float64(simulatedCadence)
					intervalDuration := time.Duration(intervalSeconds * float64(time.Second))
					timeOfNextRevolution = time.Now().Add(intervalDuration)
				}
			}
		}
	}))

	// Handler de Frequência Cardíaca.
	hrChar.HandleNotify(ble.NotifyHandlerFunc(func(req ble.Request, ntf ble.Notifier) {
        // (A lógica do handler de F.C. permanece a mesma)
		log.Printf("[SERVIDOR] ✅ App %s inscrito (F.C.)", req.Conn().RemoteAddr())
        defer log.Printf("[SERVIDOR] 🔌 App %s desinscrito (F.C.)", req.Conn().RemoteAddr())
        ticker := time.NewTicker(2 * time.Second)
        defer ticker.Stop()
        for {
            select {
			case <-ctx.Done():
				return
            case <-ntf.Context().Done():
                return
            case <-ticker.C:
                uiState.RLock()
                modifiedPower := uiState.ModifiedPower
                uiState.RUnlock()
                simulatedHR := 80 + int(float64(modifiedPower)*0.3) + rand.Intn(5) - 2
                uiState.Lock()
                uiState.HeartRate = simulatedHR
                uiState.Unlock()
                hrBytes := []byte{0x00, byte(simulatedHR)}
                if _, err := ntf.Write(hrBytes); err != nil {
                    return
                }
            }
        }
	}))

	ftmsFeatureChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) { rsp.Write([]byte{0x24, 0x00, 0x00, 0x00}) }))
	
	// 1. O HandleWrite já adiciona a propriedade "Write" automaticamente.
	ftmsControlPointChar.HandleWrite(ble.WriteHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		cmd := req.Data()
		fmt.Printf("[SERVIDOR] << Comando de controle recebido: 0x%s\n", hex.EncodeToString(cmd))

		if len(cmd) > 0 {
			opCode := cmd[0]
			if opCode == 0x00 { // Se o comando for "Request Control"
				fmt.Println("[SERVIDOR] >> Pedido de controle (0x00) recebido. App agora pode enviar comandos.")
			} else {
				commandChan <- cmd
			}
		}
	}))

	// 2. Adicionamos um HandleIndicate. Isso adiciona a propriedade "Indicate" automaticamente.
	ftmsControlPointChar.HandleIndicate(ble.NotifyHandlerFunc(func(req ble.Request, notifier ble.Notifier) {
		log.Printf("[SERVIDOR] App %s inscrito para indicações (FTMS Control Point)", req.Conn().RemoteAddr())
		<-notifier.Context().Done()
		log.Printf("[SERVIDOR] App %s desinscrito das indicações (FTMS Control Point)", req.Conn().RemoteAddr())
	}))
		
	manufacturerNameChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) { rsp.Write([]byte("Argus Framework")) }))
	modelNumberChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) { rsp.Write([]byte("GoTrainerV2")) }))

	d.AddService(powerSvc)
	d.AddService(cscSvc)
	d.AddService(hrSvc)
	d.AddService(ftmsSvc)
	d.AddService(deviceInfoSvc)

	for {
		if ctx.Err() != nil {
			break
		}
		fmt.Printf("[SERVIDOR] 📣 Anunciando como '%s'...\n", cfg.VirtualTrainerName)
		err = ble.AdvertiseNameAndServices(ctx, cfg.VirtualTrainerName, PowerSvcUUID, FTMSSvcUUID, CSCSvcUUID, HRSvcUUID, DeviceInfoSvcUUID)
		if err != nil {
			fmt.Printf("[SERVIDOR] Ciclo de anúncio terminado: %v. Reiniciando...\n", err)
		}
	}
	fmt.Println("[SERVIDOR] Encerrando a goroutine do servidor.")
}