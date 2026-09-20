# AI 工作平台核心 —— Unix 构建脚本
# 用法: make build / make run / make format / make test / make clean
#
# 目录约定：
#   core/         核心源码（不含任何构建产物）
#   sdk/          多语言插件 SDK（go / python / java / c）
#   plugins/      官方插件源码
#   runtime/      全部构建产物与运行时数据（gitignore）

SHELL := /bin/sh
ROOT  := $(shell pwd)
RT    := $(ROOT)/runtime

# 默认界面是 workbench；minimal-ui 只作为内置保底包，不进 plugins/（两者都会抢 ui.v1）
GO_PLUGINS     := fs-tool llm tool-executor agent session conversation workbench plugin-manager workspace builder imagegen svgdraw usage-monitor
BUILTIN_UIS    := minimal-ui workbench

# 启动器：产品名放在 launcher/name.txt，两个平台共用同一份
APP_NAME       := $(shell cat launcher/name.txt)
LAUNCHER       := $(ROOT)/$(APP_NAME)

.PHONY: build core go-plugins builtin launcher run format test fmt clean

build: core go-plugins builtin launcher
	@echo ""
	@echo "构建完成。"
	@echo "  启动器: $(LAUNCHER)"
	@echo "  核心:   $(RT)/core"
	@echo "  插件:"
	@ls -1 $(RT)/plugins | sed 's/^/          /'
	@echo "  内置包: $(RT)/builtin/minimal-ui.zip"

# Windows 上这一步还会往两个 exe 里写图标和版本信息（build.ps1 负责），
# Unix 没有这回事，只编一个入口出来。
launcher: core
	@echo "==> 构建启动器"
	@go build -trimpath -o "$(LAUNCHER)" ./launcher
	@chmod +x "$(LAUNCHER)"

core:
	@mkdir -p $(RT)/plugins $(RT)/plugin-configs $(RT)/logs $(RT)/pending-delete $(RT)/builtin
	@echo "==> 构建核心"
	@go build -trimpath -o $(RT)/core ./core

# ---------------------------------------------------------------------------
# Go 插件
# ---------------------------------------------------------------------------

go-plugins: core
	@for p in $(GO_PLUGINS); do \
		echo "==> 构建 go 插件 $$p"; \
		mkdir -p $(RT)/plugins/$$p.plugin; \
		go build -trimpath -o $(RT)/plugins/$$p.plugin/main ./plugins/$$p || exit 1; \
		cp plugins/$$p/manifest.json $(RT)/plugins/$$p.plugin/manifest.json; \
		chmod +x $(RT)/plugins/$$p.plugin/main; \
	done

# ---------------------------------------------------------------------------
# 内置官方 UI 包
# ---------------------------------------------------------------------------

builtin: go-plugins
	@echo "==> 打包内置 UI 包（产物只落 runtime/）"
	@mkdir -p $(RT)/builtin
	@for p in $(BUILTIN_UIS); do \
		stage=$(RT)/builtin/_stage_$$p; \
		rm -rf $$stage; mkdir -p $$stage; \
		echo "==> 构建内置 UI $$p"; \
		go build -trimpath -o $$stage/main ./plugins/$$p || exit 1; \
		cp plugins/$$p/manifest.json $$stage/ || exit 1; \
		rm -f $(RT)/builtin/$$p.zip; \
		( cd $$stage && zip -qr $(RT)/builtin/$$p.zip . ) || exit 1; \
		rm -rf $$stage; \
	done

test:
	@go test ./...
	@go vet ./...

fmt:
	@gofmt -l -w core sdk plugins

run:
	@$(LAUNCHER)

format:
	@curl -sS -X POST -H "X-Format-Token: $$(sed -n 's/.*"controlToken": *"\([^"]*\)".*/\1/p' $(RT)/core.json)" \
		http://127.0.0.1:9123/format

clean:
	@rm -rf $(RT)
