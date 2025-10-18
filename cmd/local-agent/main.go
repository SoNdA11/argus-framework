package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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

// discoverAdapters (sem alterações)
func discoverAdapters() (int, int, error) {
	log.Println("[AGENT-DISCOVERY] Procurando por 2 adaptadores BLE disponíveis...")
	var availableIDs []int
	for i := 0; i < 10; i++ {
		d, err := linux.NewDevice(ble.OptDeviceID(i))
		if err != nil { continue }
		if err := d.Stop(); err != nil { log.Printf("[AGENT-DISCOVERY] Aviso: falha ao fechar hci%d: %v", i, err) }
		log.Printf("[AGENT-DISCOVERY] ✅ Adaptador hci%d encontrado.", i)
		availableIDs = append(availableIDs, i)
		if len(availableIDs) == 2 { break }
	}
	if len(availableIDs) < 2 { return -1, -1, fmt.Errorf("falha: 2 adaptadores não encontrados") }
	clientID, serverID := availableIDs[0], availableIDs[1]
	log.Printf("[AGENT-DISCOVERY] Atribuição: hci%d (CLIENTE) | hci%d (SERVIDOR)", clientID, serverID)
	return clientID, serverID, nil
}

// writePump (sem alterações)
func writePump(ctx context.Context, c *websocket.Conn, writeChan <-chan interface{}, done chan struct{}) {
	pingTicker := time.NewTicker(30 * time.Second)
	defer func() { pingTicker.Stop(); c.Close() }()
	for {
		select {
		case <-ctx.Done():
			log.Println("[AGENT-WS] Encerrando write pump (sinal)...")
			c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return
		case <-done:
			log.Println("[AGENT-WS] Encerrando write pump (conexão perdida)...")
			return
		case msg, ok := <-writeChan:
			if !ok { log.Println("[AGENT-WS] Canal fechado."); return }
			if err := c.WriteJSON(msg); err != nil { log.Printf("[AGENT-WS] ❌ Erro escrita: %v", err); return }
		case <-pingTicker.C:
			if err := c.WriteMessage(websocket.PingMessage, nil); err != nil { log.Printf("[AGENT-WS] ❌ Erro ping: %v", err); return }
		}
	}
}

// manageBLE (Servidor Virtual) - NÃO define DefaultDevice
func manageBLE(ctx context.Context, name string, adapterID int, powerChan <-chan int, cadenceChan <-chan int, writeChan chan<- interface{}) {
	log.Printf("[AGENT-BLE] Iniciando rolo virtual no adaptador hci%d...", adapterID)
	d, err := linux.NewDevice(ble.OptDeviceID(adapterID))
	if err != nil { log.Printf("[AGENT-BLE] ❌ Falha adaptador: %s", err); writeChan <- AgentEvent{"error", map[string]interface{}{"message": err.Error()}}; return }
	// NÃO chama ble.SetDefaultDevice(d)

	powerSvc := ble.NewService(PowerSvcUUID)
	powerChar := powerSvc.NewCharacteristic(PowerCharUUID)
	powerChar.HandleNotify(ble.NotifyHandlerFunc(func(req ble.Request, ntf ble.Notifier) { /* ... lógica de notificação de potência ... */ 
		log.Printf("[AGENT-BLE] ✅ App %s inscrito Potência.", req.Conn().RemoteAddr())
		writeChan <- AgentEvent{"app_status", map[string]interface{}{"connected": true}}
		defer func() { log.Printf("[AGENT-BLE] 🔌 App %s desinscrito Potência.", req.Conn().RemoteAddr()); writeChan <- AgentEvent{"app_status", map[string]interface{}{"connected": false}} }()
		for { select { case <-ctx.Done(): return; case <-ntf.Context().Done(): return
			case watts := <-powerChan: pBytes := make([]byte, 4); binary.LittleEndian.PutUint16(pBytes[2:4], uint16(watts)); if _, err := ntf.Write(pBytes); err != nil { log.Printf("[AGENT-BLE] Erro envio potência: %v", err); return }
		}}
	}))

	cscSvc := ble.NewService(CSCSvcUUID)
	cscChar := cscSvc.NewCharacteristic(CSCMeasurementCharUUID)
	cscChar.HandleNotify(ble.NotifyHandlerFunc(func(req ble.Request, ntf ble.Notifier) { /* ... lógica de notificação de cadência ... */ 
		log.Printf("[AGENT-BLE] ✅ App %s inscrito Cadência.", req.Conn().RemoteAddr())
		defer log.Printf("[AGENT-BLE] 🔌 App %s desinscrito Cadência.", req.Conn().RemoteAddr())
		var cumRevs uint16; var lastEvtTime uint16; ticker := time.NewTicker(250*time.Millisecond); defer ticker.Stop(); var target int
		for { select { case <-ctx.Done(): return; case <-ntf.Context().Done(): return; case newTarget := <-cadenceChan: target = newTarget
			case <-ticker.C: if target <= 0 { continue }; revs := float64(target)/60.0/4.0; cumRevs += uint16(revs); lastEvtTime += (1024/4); flags := byte(0x02); buf := new(bytes.Buffer); binary.Write(buf, binary.LittleEndian, flags); binary.Write(buf, binary.LittleEndian, cumRevs); binary.Write(buf, binary.LittleEndian, lastEvtTime); if _, err := ntf.Write(buf.Bytes()); err != nil { return }
		}}
	}))

	d.AddService(powerSvc); d.AddService(cscSvc); d.AddService(ble.NewService(FTMSSvcUUID))
	log.Printf("[AGENT-BLE] 📣 Anunciando como '%s'...", name)
	if err = d.AdvertiseNameAndServices(ctx, name, PowerSvcUUID, FTMSSvcUUID, CSCSvcUUID); err != nil { log.Printf("[AGENT-BLE] Erro anunciar: %v", err) }
	log.Println("[AGENT-BLE] Anúncio parado.")
}


// --- SUBSTITUÍDO: manageTrainerConnection ---
// Agora usa a lógica EXATA de pkg/ble/client.go, adaptada.
func manageTrainerConnection(ctx context.Context, mac string, adapterID int, writeChan chan<- interface{}) {
	if mac == "" { log.Println("[AGENT-TRAINER] ⚠️ MAC não fornecido."); return }
	log.Printf("[AGENT-TRAINER] Iniciando rotina cliente (rolo real %s) via hci%d...", mac, adapterID)

	for { // Loop principal de reconexão
		if ctx.Err() != nil { log.Println("[AGENT-TRAINER] Encerrando rotina cliente."); return }

		// --- PASSO 1: Obter e definir dispositivo DENTRO do loop ---
		log.Printf("[AGENT-TRAINER] Selecionando adaptador hci%d...", adapterID)
		d, err := linux.NewDevice(ble.OptDeviceID(adapterID))
		if err != nil { log.Printf("[AGENT-TRAINER] ❌ Falha adaptador: %s. Tentando em 5s.", err); time.Sleep(5*time.Second); continue }
		ble.SetDefaultDevice(d)
		// --- FIM PASSO 1 ---

		log.Printf("[AGENT-TRAINER] 📡 Procurando por %s...", mac)
		advFilter := func(a ble.Advertisement) bool { return strings.EqualFold(a.Addr().String(), mac) }
		connectCtx, cancelConnect := context.WithTimeout(ctx, 15*time.Second)
		client, err := ble.Connect(connectCtx, advFilter) // Usa o DefaultDevice definido acima
		cancelConnect()

		if err != nil { log.Printf("[AGENT-TRAINER] Falha conectar: %v. Tentando novamente.", err); d.Stop(); time.Sleep(5*time.Second); continue }

		log.Println("[AGENT-TRAINER] ✅ Conectado ao rolo real!")
		disconnectedChan := client.Disconnected()

		// --- PASSO 2: Descobrir perfil COMPLETO (como em pkg/ble/client.go) ---
		log.Println("[AGENT-TRAINER] Aguardando 1s e descobrindo perfil completo...")
		time.Sleep(1 * time.Second) // Mantém o delay
		profile, err := client.DiscoverProfile(true) // Pede o perfil completo
		if err != nil {
			log.Printf("[AGENT-TRAINER] ❌ Falha descobrir perfil completo: %v", err)
			client.CancelConnection()
			<-disconnectedChan
			log.Println("[AGENT-TRAINER] Desconexão confirmada (falha perfil).")
			d.Stop()
			continue
		}
		// --- FIM PASSO 2 ---

		// --- PASSO 3: Encontrar característica DENTRO do perfil descoberto ---
		powerChar := findCharacteristic(profile, PowerCharUUID) // Usa a função auxiliar
		if powerChar == nil {
			log.Println("[AGENT-TRAINER] ❌ Característica potência (2A63) não encontrada no perfil.")
			client.CancelConnection()
			<-disconnectedChan
			log.Println("[AGENT-TRAINER] Desconexão confirmada (característica não encontrada).")
			d.Stop()
			continue
		}
		// --- FIM PASSO 3 ---

		log.Println("[AGENT-TRAINER] ✅ Característica 2A63 encontrada!")
		log.Println("[AGENT-TRAINER] 🔔 Inscrevendo-se para dados de potência real...")
		err = client.Subscribe(powerChar, false, func(data []byte) { /* ... lógica de envio para writeChan ... */
			if len(data) >= 4 { pVal := binary.LittleEndian.Uint16(data[2:4]); select { case writeChan <- AgentEvent{"trainer_data", map[string]interface{}{"real_power": int(pVal)}}: default: log.Println("[AGENT-TRAINER] Aviso: Canal escrita cheio.") } }
		})
		if err != nil { log.Printf("[AGENT-TRAINER] ❌ Falha inscrever: %v", err); client.CancelConnection(); <-disconnectedChan; log.Println("[AGENT-TRAINER] Desconexão confirmada (falha inscrição)."); d.Stop(); continue }

		log.Println("[AGENT-TRAINER] Inscrição OK. Monitorando...")
		select { // Espera desconexão ou cancelamento
		case <-disconnectedChan: log.Println("[AGENT-TRAINER] 🔌 Desconectado. Reconectando...")
		case <-ctx.Done(): log.Println("[AGENT-TRAINER] Contexto cancelado. Desconectando..."); client.CancelConnection(); <-disconnectedChan; log.Println("[AGENT-TRAINER] Desconexão confirmada (cancelamento).")
		}
		d.Stop() // Libera o dispositivo antes da próxima iteração
	}
}


// findCharacteristic (agora é realmente usado)
func findCharacteristic(p *ble.Profile, uuid ble.UUID) *ble.Characteristic {
	for _, s := range p.Services {
		for _, c := range s.Characteristics {
			if c.UUID.Equal(uuid) {
				return c
			}
		}
	}
	return nil
}


// main (sem alterações)
func main() {
	agentKey := flag.String("key", "", "Chave API")
	trainerMAC := flag.String("mac", "", "MAC Rolo Real")
	flag.Parse()
	if *agentKey == "" { log.Fatal("❌ --key obrigatória") }
	addr := "wss://argus-remote-server.onrender.com/agent"
	clientAdapterID, serverAdapterID, err := discoverAdapters(); if err != nil { log.Fatalf("❌ %v", err) }
	log.Printf("[AGENT] Iniciando... Cliente(hci%d) -> Servidor(hci%d)", clientAdapterID, serverAdapterID)
	interrupt := make(chan os.Signal, 1); signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background()); go func() { <-interrupt; log.Println("Encerrando..."); cancel() }()
	powerChan := make(chan int, 10); cadenceChan := make(chan int, 10)

	for { // Loop de conexão Websocket
		if ctx.Err() != nil { log.Println("Contexto cancelado. Saindo."); return }
		log.Printf("[AGENT] Conectando a %s", addr)
		c, _, err := websocket.DefaultDialer.Dial(addr, nil); if err != nil { log.Println("❌ Falha WS:", err); time.Sleep(5 * time.Second); continue }
		log.Println("[AGENT] ✅ Conectado WS! Autenticando...")
		authMsg := map[string]string{"agent_key": *agentKey}; if err := c.WriteJSON(authMsg); err != nil { log.Println("❌ Falha auth:", err); c.Close(); time.Sleep(5 * time.Second); continue }
		writeChan := make(chan interface{}, 10); done := make(chan struct{}); bleCtx, bleCancel := context.WithCancel(ctx)
		go writePump(ctx, c, writeChan, done)

		go func() { // Goroutine de leitura WS e disparo BLE
			defer func() { bleCancel(); close(done) }()
			for { var cmd AgentCommand; if err := c.ReadJSON(&cmd); err != nil { log.Println("🔌 Erro leitura WS:", err); return }
				switch cmd.Action {
				case "start_virtual_trainer": if name, ok := cmd.Payload["name"].(string); ok {
					log.Println("[AGENT] Comando 'start_virtual_trainer' recebido.")
					go manageTrainerConnection(bleCtx, *trainerMAC, clientAdapterID, writeChan) // Cliente define DefaultDevice
					go manageBLE(bleCtx, name, serverAdapterID, powerChan, cadenceChan, writeChan) // Servidor usa métodos específicos
				}
				case "send_power": if watts, ok := cmd.Payload["watts"].(float64); ok { select { case powerChan <- int(watts): default: } }
				case "send_cadence": if rpm, ok := cmd.Payload["rpm"].(float64); ok { select { case cadenceChan <- int(rpm): default: } }
				}
			}
		}()

		select { // Espera leitura WS falhar ou Ctrl+C
		case <-done: log.Println("[AGENT] Conexão WS perdida. Reconectando...")
		case <-ctx.Done(): log.Println("Sinal recebido. Fechando..."); time.Sleep(1 * time.Second); return
		}
	}
}