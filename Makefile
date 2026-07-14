.PHONY: all ui go clean

APP_NAME := igonotes
BUILD_DIR := builds

# Полная сборка (по умолчанию)
all: ui go

# Сборка только UI (фронтенд)
ui:
	@echo "=> Сборка UI (Svelte/Vite)..."
	cd web && npm install && npm run build

# Сборка только Go (бэкенд)
go:
	@echo "=> Сборка Go бинарника..."
	mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(APP_NAME) ./cmd/...

# Очистка артефактов сборки
clean:
	@echo "=> Очистка..."
	rm -rf $(BUILD_DIR)
	rm -rf web/dist
