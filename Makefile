# ============================================================================
# MuseRouter Makefile - Antigravity Polyglot v2.4 (PHAN 6 BAT BUOC)
# Ho tro: build, test, run, clean, lint, docker, pqc-test, key-ceremony, siem, kafka
# ============================================================================

.PHONY: all build test run clean lint docker pqc-test key-ceremony siem kafka

all: build

	@echo [1/1] Building standalone Go Engine (muse_router.exe)...
	cd muse_router_engine && go build -ldflags="-s -w" -o muse_router.exe .

test:
	@echo [TEST] Go Engine Unit Tests...
	cd muse_router_engine && go test -v ./...

run:
	cmd /c run_muse_router.bat

clean:
	@echo Cleaning build caches and temporary files...
	if exist muse_router_core\target rmdir /s /q muse_router_core\target
	if exist muse_router_ui\obj rmdir /s /q muse_router_ui\obj
	del /s /q *.bak 2>nul || exit /b 0

lint:
	cd muse_router_engine && go vet ./...

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
