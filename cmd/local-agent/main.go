package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
	"flag"
	"fmt"

	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux"
	"github.com/gorilla/websocket"
)

type AgentCommand struct {
	Action  string                 `json:"action"`
	Payload map[string]interface{} `json:"payload"`
}
type AgentEvent struct {
	Event   string                 `json:"event"`
	Payload map[string]interface{} `json:"payload"`
}

var (
	PowerSvcUUID           = ble.MustParse("00001818-0000-1000-8000-00805f9b34fb")
	PowerCharUUID          = ble.MustParse("00002a63-0000-1000-8000-00805f9b34fb")
	CSCSvcUUID             = ble.MustParse("00001816-0000-1000-8000-00805f9b34fb")
	CSCMeasurementCharUUID = ble.MustParse("00002a5b-0000-1000-8000-00805f9b34fb")
	FTMSSvcUUID            = ble.MustParse("00001826-0000-1000-8000-00805f9b34fb")
)

// discoverAdapter procura por UM adaptador BLE funcional no sistema.
func discoverAdapter() (int, error) {
	log.Println("[AGENT-DISCOVERY] Procurando por adaptador BLE disponível...")

	// Tenta de hci0 a hci9
	for i := 0; i < 10; i++ {
		// Tenta inicializar o dispositivo
		d, err := linux.NewDevice(ble.OptDeviceID(i))
		if err != nil {
			// Se falhar (ex: "no such device" ou "RF-kill"), ignora e continua
			continue
		}

		// Se funcionou, fecha/para o dispositivo para liberar o recurso
		if err := d.Stop(); err != nil {
			log.Printf("[AGENT-DISCOVERY] Aviso: falha ao parar hci%d após teste: %v", i, err)
		}
		
		log.Printf("[AGENT-DISCOVERY] ✅ Adaptador hci%d encontrado e disponível.", i)
		return i, nil // Retorna o ID do primeiro que encontrar
	}

	// Se o loop terminar, nenhum foi encontrado
	return -1, fmt.Errorf("falha na descoberta: nenhum adaptador BLE disponível foi encontrado (verifique conexões e RF-kill)")
}

func manageBLE(ctx context.Context, name string, adapterID int, powerChan <-chan int, cadenceChan <-chan int, ws *websocket.Conn) {
	log.Printf("[AGENT-BLE] Iniciando rolo virtual no adaptador hci%d...", adapterID)
	d, err := linux.NewDevice(ble.OptDeviceID(adapterID))
	if err != nil { 
		log.Printf("[AGENT-BLE] ❌ Falha ao selecionar adaptador: %s", err)
		// Reporta o erro ao servidor (opcional, mas bom)
		ws.WriteJSON(AgentEvent{"error", map[string]interface{}{"message": err.Error()}})
		return 
	}
	ble.SetDefaultDevice(d)

	powerSvc := ble.NewService(PowerSvcUUID)
	powerChar := powerSvc.NewCharacteristic(PowerCharUUID)
	powerChar.HandleNotify(ble.NotifyHandlerFunc(func(req ble.Request, ntf ble.Notifier) {
		log.Printf("[AGENT-BLE] ✅ App %s inscrito para Potência.", req.Conn().RemoteAddr())
		ws.WriteJSON(AgentEvent{"app_status", map[string]interface{}{"connected": true}})
		defer func() {
			log.Printf("[AGENT-BLE] 🔌 App %s desinscrito da Potência.", req.Conn().RemoteAddr())
			ws.WriteJSON(AgentEvent{"app_status", map[string]interface{}{"connected": false}})
		}()
		for {
			select {
			case <-ctx.Done(): return
			case <-ntf.Context().Done(): return
			case watts := <-powerChan:
				powerBytes := make([]byte, 4)
				binary.LittleEndian.PutUint16(powerBytes[2:4], uint16(watts))
				if _, err := ntf.Write(powerBytes); err != nil { log.Printf("[AGENT-BLE] Erro ao enviar potência: %v", err) }
			}
		}
	}))

	cscSvc := ble.NewService(CSCSvcUUID)
	cscChar := cscSvc.NewCharacteristic(CSCMeasurementCharUUID)
	cscChar.HandleNotify(ble.NotifyHandlerFunc(func(req ble.Request, ntf ble.Notifier) {
		log.Printf("[AGENT-BLE] ✅ App %s inscrito para Cadência.", req.Conn().RemoteAddr())
		defer log.Printf("[AGENT-BLE] 🔌 App %s desinscrito da Cadência.", req.Conn().RemoteAddr())
		
		var cumulativeRevolutions uint16
		var lastCrankEventTime uint16

		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		
		var cadenciaAlvo int

		for {
			select {
			case <-ctx.Done(): return
			case <-ntf.Context().Done(): return
			case novoAlvo := <-cadenceChan:
				cadenciaAlvo = novoAlvo
			case <-ticker.C:
				if cadenciaAlvo <= 0 { continue }
				
				revolutionsInInterval := float64(cadenciaAlvo) / 60.0 / 4.0
				cumulativeRevolutions += uint16(revolutionsInInterval)
				lastCrankEventTime += (1024 / 4)

				flags := byte(0x02)
				buf := new(bytes.Buffer)
				binary.Write(buf, binary.LittleEndian, flags)
				binary.Write(buf, binary.LittleEndian, cumulativeRevolutions)
				binary.Write(buf, binary.LittleEndian, lastCrankEventTime)
				if _, err := ntf.Write(buf.Bytes()); err != nil { return }
			}
		}
	}))

	d.AddService(powerSvc)
	d.AddService(cscSvc)
	d.AddService(ble.NewService(FTMSSvcUUID))
	
	log.Printf("[AGENT-BLE] 📣 Anunciando como '%s'...", name)
	if err = ble.AdvertiseNameAndServices(ctx, name, PowerSvcUUID, FTMSSvcUUID, CSCSvcUUID); err != nil {
		log.Printf("[AGENT-BLE] Erro ao anunciar: %v", err)
	}
	log.Println("[AGENT-BLE] Anúncio parado.")
}

func main() {
    // 1. MUDANÇA: O padrão agora é -1 (para auto-descoberta)
    adapterFlag := flag.Int("adapter", -1, "ID do adaptador HCI (ex: 0). Padrão -1 para auto-descoberta.")
    flag.Parse() // Faz a leitura da flag

	addr := "wss://argus-remote-server.onrender.com/agent"
	
	var finalAdapterID int // O ID que realmente usaremos

	if *adapterFlag == -1 {
		// Modo de auto-descoberta
		id, err := discoverAdapter()
		if err != nil {
			log.Fatalf("❌ %v", err) // Para o programa se nenhum adaptador for encontrado
		}
		finalAdapterID = id
	} else {
		// Modo manual (usuário especificou a flag)
		log.Printf("[AGENT] Usando adaptador manual hci%d conforme flag.", *adapterFlag)
		finalAdapterID = *adapterFlag
	}

	log.Printf("[AGENT] Iniciando agente local no adaptador hci%d...", finalAdapterID)

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		<-interrupt
		log.Println("Sinal de interrupção recebido, encerrando agente...")
		cancel()
	}()

	powerChan := make(chan int, 10)
	cadenceChan := make(chan int, 10)

	for {
		if ctx.Err() != nil {
			log.Println("Contexto cancelado. Saindo do loop de reconexão.")
			return
		}

		log.Printf("[AGENT] Tentando se conectar a %s", addr)
		c, _, err := websocket.DefaultDialer.Dial(addr, nil)
		if err != nil {
			log.Println("[AGENT] ❌ Falha ao conectar, tentando novamente em 5 segundos:", err)
			time.Sleep(5 * time.Second)
			continue 
		}
		log.Println("[AGENT] ✅ Conectado ao Servidor Remoto!")

		done := make(chan struct{})
		
		go func() {
			defer close(done) 
			for {
				var cmd AgentCommand
				if err := c.ReadJSON(&cmd); err != nil {
					log.Println("[AGENT] 🔌 Erro de leitura (desconectado):", err)
					return 
				}

				switch cmd.Action {
				case "start_virtual_trainer":
					log.Printf("[AGENT] << Comando '%s' recebido!", cmd.Action)
					if name, ok := cmd.Payload["name"].(string); ok {
						// CORREÇÃO APLICADA AQUI:
						// Usa o 'finalAdapterID' (descoberto ou da flag)
						go manageBLE(ctx, name, finalAdapterID, powerChan, cadenceChan, c)
					}
				case "send_power":
					if watts, ok := cmd.Payload["watts"].(float64); ok {
						powerChan <- int(watts)
					}
				case "send_cadence":
					if rpm, ok := cmd.Payload["rpm"].(float64); ok {
						select {
						case cadenceChan <- int(rpm):
						default:
						}
					}
				default:
					log.Printf("[AGENT] << Comando desconhecido: %s", cmd.Action)
				}
			}
		}()

		select {
		case <-done:
			log.Println("[AGENT] Conexão perdida. Tentando reconectar...")
			c.Close() 
		case <-ctx.Done():
			log.Println("Sinal de encerramento recebido. Fechando conexão WebSocket...")
			c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			time.Sleep(1 * time.Second) 
			return                      
		}
	}
}