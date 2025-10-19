// Package ble contém toda a lógica Bluetooth Low Energy (BLE) do projeto.
package ble

import (
	"strings"
	"sync"
	"time"
	
	"github.com/go-ble/ble"
)

// --- ESTRUTURAS DE ESTADO E CONFIGURAÇÃO ---
// Apenas as definições dos tipos. As instâncias serão criadas em main.go.
// 'sync.RWMutex' é usado em todas para garantir acesso seguro por múltiplas goroutines ao mesmo tempo.
type AttackConfig struct {
	sync.RWMutex
	Active   bool   // Se o ataque de boost está ligado.
	Mode     string // "aditivo" ou "percentual".
	Value    int    // Campo antigo, pode ser removido.
	ValueMin int    // Valor mínimo para o range do ataque.
	ValueMax int    // Valor máximo para o range do ataque.
}

// ResistanceConfig controla se os comandos de resistência devem ser encaminhados.
type ResistanceConfig struct {
	sync.RWMutex
	Forward bool
}

// UIState armazena os dados dinâmicos que mudam a todo momento durante a execução.
type UIState struct {
	sync.RWMutex
	ClientConnected bool // Status da conexão com o rolo real.
	AppConnected    bool // Status da conexão com o app (Zwift/MyWhoosh).
	RealPower       int  // Potência lida do rolo real.
	ModifiedPower   int  // Potência final enviada para o app.
	HeartRate       int  // Frequência cardíaca simulada.
	RealCadence     int  //	Cadência lida do rolo real
	MainMode        string // +++ ADICIONADO: "boost" ou "bot"
	// Para lógica de boost dinâmico
	CurrentBoostTarget  int
	NextBoostChangeTime time.Time
}

// --- CONSTANTES E UUIDs BLE ---
// UUIDs são os "endereços" universais para serviços e características Bluetooth.
const (
	// UUID completo da característica de medição de potência.
	PowerCharUUIDStr = "00002a63-0000-1000-8000-00805f9b34fb"
)

// 'var' agrupa todas as constantes de UUIDs que o seu rolo virtual vai usar.
var (
	// Serviços
	PowerSvcUUID      = ble.MustParse("00001818-0000-1000-8000-00805f9b34fb") // Serviço de Potência de Ciclismo
	CSCSvcUUID        = ble.MustParse("00001816-0000-1000-8000-00805f9b34fb") // Serviço de Velocidade e Cadência de Ciclismo
	FTMSSvcUUID       = ble.MustParse("00001826-0000-1000-8000-00805f9b34fb") // Serviço de Máquina de Fitness (para controle)
	DeviceInfoSvcUUID = ble.MustParse("0000180a-0000-1000-8000-00805f9b34fb") // Serviço de Informações do Dispositivo
	HRSvcUUID         = ble.MustParse("0000180d-0000-1000-8000-00805f9b34fb") // Serviço de Frequência Cardíaca

	// Características
	PowerCharUUID            = ble.MustParse(PowerCharUUIDStr)                     // Medição de Potência
	CSCMeasurementCharUUID   = ble.MustParse("00002a5b-0000-1000-8000-00805f9b34fb") // Medição de Velocidade e Cadência
	FTMSFeatureCharUUID      = ble.MustParse("00002acc-0000-1000-8000-00805f9b34fb") // Recursos da Máquina de Fitness
	FTMSControlPointCharUUID = ble.MustParse("00002ad9-0000-1000-8000-00805f9b34fb") // Ponto de Controle (para receber comandos)
	ManufacturerNameCharUUID = ble.MustParse("00002a29-0000-1000-8000-00805f9b34fb") // Nome do Fabricante
	ModelNumberCharUUID      = ble.MustParse("00002a24-0000-1000-8000-00805f9b34fb") // Número do Modelo
	HRMeasurementCharUUID    = ble.MustParse("00002a37-0000-1000-8000-00805f9b34fb") // Medição de Frequência Cardíaca
)

// FindCharacteristic é uma função auxiliar para encontrar uma característica dentro de um perfil BLE.
func FindCharacteristic(p *ble.Profile, uuidStr string) *ble.Characteristic {
	targetUUID := strings.ToLower(strings.ReplaceAll(uuidStr, "-", ""))
	for _, s := range p.Services {
		for _, c := range s.Characteristics {
			foundUUID := strings.ToLower(strings.ReplaceAll(c.UUID.String(), "-", ""))
			if strings.Contains(targetUUID, foundUUID) {
				return c
			}
		}
	}
	return nil
}