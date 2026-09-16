# API — Contratos para integração

Documentação das rotas HTTP do **mapperScrapy** para consumo por outros serviços.

**Base URL (local):** `http://localhost:8080`

**Content-Type:** `application/json`

---

## Visão geral

| Método | Rota | Descrição |
|--------|------|-----------|
| `POST` | `/mapper/` | Busca estabelecimentos no Google Maps por cidade + segmento |

Timeouts longos são esperados (scraping headless). Recomenda-se timeout de cliente ≥ **10 minutos**.

---

## `POST /mapper/`

Busca locais no Google Maps no Brasil, a partir de cidade e segmento, e retorna até `limit` estabelecimentos enriquecidos.

### Request body

| Campo | Tipo | Obrigatório | Default | Descrição |
|-------|------|-------------|---------|-----------|
| `city` | `string` | sim | — | Cidade (ex.: `"São Paulo"`, `"corrente"`) |
| `segment` | `string` | sim | — | Segmento/categoria (ex.: `"padaria"`, `"farmácia"`) |
| `limit` | `int` | não | `100` | Quantidade máxima de places (1–100) |

#### Exemplo

```json
{
  "city": "São Paulo",
  "segment": "padaria",
  "limit": 20
}
```

### Response `200 OK`

| Campo | Tipo | Descrição |
|-------|------|-----------|
| `city` | `string` | Cidade informada na request |
| `count` | `int` | Quantidade de places retornados |
| `places` | `Place[]` | Lista de estabelecimentos |

#### Objeto `Place`

| Campo | Tipo | Sempre presente | Descrição |
|-------|------|-----------------|-----------|
| `id` | `string` | preferencialmente | ID do place (ex.: `ChIJ...` ou `0x...:0x...`) |
| `name` | `string` | sim | Nome do estabelecimento |
| `phone` | `string` | não | Telefone, quando disponível no Maps |
| `email` | `string` | não | E-mail (`mailto:`), raro no Maps |
| `address` | `string` | não | Endereço |
| `lat` | `number` | não | Latitude |
| `lng` | `number` | não | Longitude |
| `website` | `string` | não | Site oficial |
| `category` | `string` | não | Categoria exibida no Maps |
| `rating` | `number` | não | Avaliação (ex.: `4.6`) |
| `photoUrl` | `string` | não | URL da foto principal |

Campos opcionais usam `omitempty` e podem ser omitidos quando vazios/`0`.

#### Exemplo de response

```json
{
  "city": "São Paulo",
  "count": 2,
  "places": [
    {
      "id": "ChIJN1t_tDeuEmsRUsoyG83frY4",
      "name": "Padaria Pão Quente",
      "phone": "+55 11 3456-7890",
      "email": "contato@paoquente.com.br",
      "address": "Rua das Flores, 123 - Centro, São Paulo - SP, 01000-000, Brazil",
      "lat": -23.55052,
      "lng": -46.633308,
      "website": "https://www.paoquente.com.br",
      "category": "Padaria",
      "rating": 4.6,
      "photoUrl": "https://lh5.googleusercontent.com/p/exemplo"
    }
  ]
}
```

### Erros

| Status | Quando |
|--------|--------|
| `400 Bad Request` | JSON inválido, `city`/`segment` vazios, ou `limit > 100` |
| `405 Method Not Allowed` | Método diferente de `POST` |
| `502 Bad Gateway` | Falha no scraping (timeout, bloqueio, feed sem resultados, etc.) |

Corpo do erro: texto plain (`text/plain`), com a mensagem da falha.

#### Exemplos de erro

```text
city is required
```

```text
limit must be <= 100
```

```text
google maps blocked scraping (captcha); debug: tmp/debug/...
```

---

## Exemplos de integração

### cURL

```bash
curl -X POST http://localhost:8080/mapper/ \
  -H "Content-Type: application/json" \
  -d '{"city":"São Paulo","segment":"padaria","limit":20}' \
  --max-time 900
```

### Go

```go
reqBody := []byte(`{"city":"São Paulo","segment":"padaria","limit":20}`)
req, _ := http.NewRequest(http.MethodPost, "http://localhost:8080/mapper/", bytes.NewReader(reqBody))
req.Header.Set("Content-Type", "application/json")

client := &http.Client{Timeout: 15 * time.Minute}
resp, err := client.Do(req)
// ...
```

### Node.js (fetch)

```js
const res = await fetch("http://localhost:8080/mapper/", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({
    city: "São Paulo",
    segment: "padaria",
    limit: 20,
  }),
  signal: AbortSignal.timeout(15 * 60 * 1000),
});

const data = await res.json();
```

---

## Boas práticas para consumidores

1. Sempre use timeout de cliente alto (scraping não é instantâneo).
2. Trate `502` como falha transitória/bloqueio do Maps; logs do serviço e `tmp/debug/` ajudam no diagnóstico.
3. Não assuma que `count == limit` — o Maps pode retornar menos resultados para a cidade/segmento.
4. Campos como `email`, `phone` e `website` dependem do que o Maps exibe publicamente.
5. A API atual **não** exige autenticação.
