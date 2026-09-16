# mapperScrapy

Serviço HTTP em Go que busca estabelecimentos no Google Maps a partir de uma **cidade** e um **segmento** (ex.: padaria, farmácia), retornando dados estruturados para consumo por outros serviços.

> Escopo atual: buscas focadas no **Brasil**.

## Sumário

- [Requisitos](#requisitos)
- [Instalação](#instalação)
- [Como rodar](#como-rodar)
- [Exemplo rápido](#exemplo-rápido)
- [Estrutura do projeto](#estrutura-do-projeto)
- [Documentação](#documentação)
- [Observações](#observações)

## Requisitos

- [Go](https://go.dev/dl/) **1.22+** (o módulo declara `go 1.27.1`)
- Google Chrome / Chromium instalado (usado pelo [chromedp](https://github.com/chromedp/chromedp) em modo headless)
- macOS, Linux ou Windows

## Instalação

```bash
git clone <url-do-repositorio> mapperScrapy
cd mapperScrapy
go mod download
```

## Como rodar

### Opção 1 — direto com Go

```bash
go run .
```

O servidor sobe em `http://localhost:8080`.

### Opção 2 — build + binário

```bash
go build -o mapper .
./mapper
```

### Opção 3 — hot reload com Air (opcional)

Se tiver [Air](https://github.com/air-verse/air) instalado e o arquivo `.air.toml` no projeto:

```bash
air
```

## Exemplo rápido

```bash
curl -X POST http://localhost:8080/mapper/ \
  -H "Content-Type: application/json" \
  -d '{
    "city": "São Paulo",
    "segment": "padaria",
    "limit": 20
  }'
```

A request pode demorar vários minutos (scraping + enrich paralelo). Configure timeout alto no cliente (recomendado: **10+ minutos**).

## Estrutura do projeto

```text
mapperScrapy/
├── main.go              # sobe o HTTP server e faz o wiring
├── handler/             # camada HTTP (request/response)
├── service/             # orquestração de negócio
├── scraper/             # scraping Google Maps (chromedp + workers)
├── model/               # DTOs de request/response
├── docs/                # documentação da API e do scraper
├── go.mod
└── README.md
```

Fluxo: **Handler → Service → Scraper**.

## Documentação

| Documento | Conteúdo |
|-----------|----------|
| [docs/api.md](docs/api.md) | Rotas HTTP, contratos e exemplos para integração |
| [docs/scraper.md](docs/scraper.md) | Como o scraper funciona e a arquitetura multithread |

## Observações

- O Google Maps pode limitar resultados, exibir consent/CAPTCHA ou alterar a UI — o scraper grava artefatos de debug em `tmp/debug/` quando falha.
- `limit` máximo: **100**.
- Não há autenticação na API neste momento; use apenas em rede confiável.
