package ollama

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client клиент для работы с Ollama API
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient создает новый клиент Ollama
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// TrainingRequest запрос на обучение модели
type TrainingRequest struct {
	Model        string                 `json:"model"`
	Data         []TrainingSequence     `json:"data"`
	Epochs       int                    `json:"epochs"`
	LearningRate float64                `json:"learning_rate"`
	Options      map[string]interface{} `json:"options,omitempty"`
}

// TrainingSequence последовательность для обучения
type TrainingSequence struct {
	Input  string `json:"input"`  // Входные данные в формате строки
	Output string `json:"output"` // Ожидаемый выход
}

// TrainingResponse ответ от Ollama на запрос обучения
type TrainingResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Model   string `json:"model,omitempty"`
}

// PredictionRequest запрос на предсказание
type PredictionRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

// PredictionResponse ответ с предсказанием
type PredictionResponse struct {
	Response string `json:"response"`
	Model    string `json:"model"`
}

// TrainModel обучает модель на предоставленных данных
func (c *Client) TrainModel(req TrainingRequest) (*TrainingResponse, error) {
	url := fmt.Sprintf("%s/api/train", c.baseURL)

	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("ошибка сериализации запроса: %w", err)
	}

	httpReq, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("ошибка создания HTTP запроса: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ошибка выполнения HTTP запроса: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ошибка сервера Ollama (код %d): %s", resp.StatusCode, string(body))
	}

	var trainingResp TrainingResponse
	if err := json.NewDecoder(resp.Body).Decode(&trainingResp); err != nil {
		return nil, fmt.Errorf("ошибка парсинга ответа: %w", err)
	}

	return &trainingResp, nil
}

// Predict делает предсказание с помощью обученной модели
func (c *Client) Predict(req PredictionRequest) (*PredictionResponse, error) {
	url := fmt.Sprintf("%s/api/generate", c.baseURL)

	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("ошибка сериализации запроса: %w", err)
	}

	httpReq, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("ошибка создания HTTP запроса: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ошибка выполнения HTTP запроса: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ошибка сервера Ollama (код %d): %s", resp.StatusCode, string(body))
	}

	var predictionResp PredictionResponse
	if err := json.NewDecoder(resp.Body).Decode(&predictionResp); err != nil {
		return nil, fmt.Errorf("ошибка парсинга ответа: %w", err)
	}

	return &predictionResp, nil
}

// HealthCheck проверяет доступность Ollama сервера
func (c *Client) HealthCheck() error {
	url := fmt.Sprintf("%s/api/tags", c.baseURL)

	resp, err := c.httpClient.Get(url)
	if err != nil {
		return fmt.Errorf("ошибка подключения к Ollama: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Ollama сервер недоступен (код %d)", resp.StatusCode)
	}

	return nil
}

