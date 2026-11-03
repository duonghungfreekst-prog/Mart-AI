# ============================================================================
# MuseRouter Makefile - Antigravity Polyglot v2.4 (PHAN 6 BAT BUOC)
# Ho tro: build, test, run, clean, lint, docker, pqc-test, key-ceremony, siem, kafka
# ============================================================================

.PHONY: all build test run clean lint docker pqc-test key-ceremony siem kafka

all: build

build:
	@echo [1/3] Building Rust Core DLL (muse_router_core.dll)...
	cd muse_router_core && cargo build --release && copy /Y target\release\muse_router_core.dll muse_router_core.dll
	@echo [2/3] Building Go Engine (muse_router.exe)...
	cd muse_router_engine && go build -o muse_router.exe .
	@echo [3/3] Building C# WPF UI (MuseRouter.exe)...
	cd muse_router_ui && dotnet build -c Release -o bin\Release

test:
	@echo [TEST 1/4] Rust Core Unit Tests...
	cd muse_router_core && cargo test
	@echo [TEST 2/4] Python AI Bridge Unit Tests...
	cd muse_router_ai && python -m unittest test_bridge_service.py -v
	@echo [TEST 3/4] Go Engine Unit Tests...
	cd muse_router_engine && go test -v ./...
	@echo [TEST 4/4] C# UI Unit Tests...
	cd muse_router_ui && bin\Release\MuseRouter.exe --run-tests

run:
	cmd /c run_muse_router.bat

clean:
	@echo Cleaning build caches and temporary files...
	if exist muse_router_core\target rmdir /s /q muse_router_core\target
	if exist muse_router_ui\obj rmdir /s /q muse_router_ui\obj
	del /s /q *.bak 2>nul || exit /b 0

lint:
	cd muse_router_engine && go vet ./...
	cd muse_router_core && cargo check
	cd muse_router_ai && python -m py_compile bridge_service.py test_bridge_service.py

docker:
	@echo [DOCKER] Building containerized MuseRouter services...
	docker --version

pqc-test:
	@echo [PQC] Running Post-Quantum & AES-256-CTR+HMAC-SHA256 vault verification...
	cd muse_router_core && cargo test

key-ceremony:
	@echo [KEY-CEREMONY] Verifying RS256 asymmetric keypair & Rust Core Vault integrity...
	cd muse_router_engine && go test -v -run TestJWTRS256SignAndVerify

siem:
	@echo [SIEM] Auditing structured logs ([UI], [ENGINE], [AI], [CORE])...
	cd muse_router_engine && go test -v -run TestInputValidationAndSecurityHeaders

kafka:
	@echo [KAFKA] Verifying Idempotency Key middleware and bounded worker queues...
	cd muse_router_engine && go test -v -run TestRustFFIAndRouterPoolFlow
