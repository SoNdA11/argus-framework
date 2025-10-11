// pkg/config/config.go

// Package config gerencia o carregamento de configurações estáticas do aplicativo.

package config

import (
	"encoding/json"
	"os"
)

// AppConfig define a estrutura do arquivo de configuração config.json.
// Estes são os parâmetros que não mudam durante a execução do programa.

type AppConfig struct {
	ClientAdapterID    int    `json:"client_adapter_id"`
	ServerAdapterID    int    `json:"server_adapter_id"`
	TrainerMAC         string `json:"trainer_mac"`
	VirtualTrainerName string `json:"virtual_trainer_name"`
}

// Load lê um arquivo de configuração do caminho fornecido e retorna uma struct AppConfig.
func Load(path string) (*AppConfig, error) {
	// Abre o arquivo JSON do caminho especificado (ex: "configs/config.json").
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	// Garante que o arquivo seja fechado ao final da função.
	defer file.Close()

	// Cria uma instância vazia da struct de configuração.
	cfg := &AppConfig{}

	// Cria um decodificador JSON que lê do arquivo.
	decoder := json.NewDecoder(file)

	// Decodifica (converte) o JSON do arquivo para a struct cfg.
	if err := decoder.Decode(cfg); err != nil {
		return nil, err
	}
	
	// Retorna a struct de configuração preenchida e nenhum erro.
	return cfg, nil
}