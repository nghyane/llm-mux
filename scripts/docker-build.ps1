# docker-build.ps1 - Build and run llm-mux Docker container

$ErrorActionPreference = "Stop"

Write-Host "Please select an option:"
Write-Host "1) Run using Pre-built Image (Recommended)"
Write-Host "2) Build from Source and Run (For Developers)"
$choice = Read-Host -Prompt "Enter choice [1-2]"

switch ($choice) {
    "1" {
        Write-Host "--- Running with Pre-built Image ---"
        docker compose up -d --remove-orphans --no-build
        Write-Host "Services are starting from remote image."
        Write-Host "Run 'docker compose logs -f' to see the logs."
    }
    "2" {
        Write-Host "--- Building from Source and Running ---"

        $VERSION = (git describe --tags --always --dirty 2>$null) ?? "dev"
        $COMMIT = (git rev-parse --short HEAD 2>$null) ?? "none"
        $BUILD_DATE = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")

        Write-Host "Building with:"
        Write-Host "  Version: $VERSION"
        Write-Host "  Commit: $COMMIT"
        Write-Host "  Build Date: $BUILD_DATE"

        $env:LLM_MUX_IMAGE = "llm-mux:local"

        docker compose build --build-arg VERSION=$VERSION --build-arg COMMIT=$COMMIT --build-arg BUILD_DATE=$BUILD_DATE

        docker compose up -d --remove-orphans --pull never

        Write-Host "Build complete. Run 'docker compose logs -f' to see logs."
    }
    default {
        Write-Host "Invalid choice. Please enter 1 or 2."
        exit 1
    }
}
