# Scraper e arquitetura multithread

Este documento descreve como o scraper do Google Maps funciona dentro do **mapperScrapy**, incluindo a coleta do feed e o enrich paralelo com workers.

## Visão geral

O objetivo do scraper é, a partir de `city` + `segment`:

1. Abrir o Google Maps (Chrome headless via **chromedp**)
2. Coletar links de estabelecimentos no feed de resultados
3. Abrir cada place e extrair dados detalhados
4. Devolver uma lista de `Place` para a API

Implementação principal: `scraper/google_maps.go`.

## Camadas envolvidas

```text
HTTP Request
    │
    ▼
handler/          valida JSON e aplica timeout da request
    │
    ▼
service/          orquestra a busca (city, segment, limit)
    │
    ▼
scraper/          Chrome headless + coleta + enrich paralelo
    │
    ▼
[]model.Place
```

## Fluxo do scraper (passo a passo)

### 1. Montagem da busca

Query usada:

```text
{segment} em {city}, Brasil
```

URL:

```text
https://www.google.com/maps/search/{query}?hl=pt-BR
```

### 2. Browser headless

- Um **ExecAllocator** sobe o Chrome com flags headless.
- Um **browser context** é o “dono” do processo do Chrome (não pode ser cancelado cedo).
- Uma **aba de feed** navega para a busca, trata consentimento de cookies e espera resultados.

Se o Maps bloquear (CAPTCHA / bot detection), o scraper falha e pode gravar screenshot/HTML em `tmp/debug/`.

### 3. Coleta de links no feed (`collectPlaceLinks`)

Comportamento:

- Lê `a[href*="/maps/place/"]` dentro de `div[role="feed"]`
- Faz scroll agressivo (`scrollIntoView`, `wheel`, `scrollTop`)
- Espera lazy-load de novos itens
- Deduplica por chave estável (place id / path da URL)
- Para quando atinge `limit`, detecta fim da lista, ou esgota tentativas sem novos links

Observação: a quantidade final depende do que o Google Maps disponibiliza para aquela cidade/segmento. Cidades pequenas podem retornar bem menos que 100.

### 4. Enrich paralelo (`enrichPlacesParallel`)

Depois de ter os links, o scraper **não** abre os places em série. Ele usa um pool de workers.

#### Parâmetros atuais

| Constante | Valor | Papel |
|-----------|-------|-------|
| `maxPlaces` | `100` | Limite máximo de places |
| `enrichWorkers` | `5` | Número de tabs/workers em paralelo |
| timeout dinâmico | ~6–15 min | Orçamento total da busca |

#### Diagrama

```text
                    ┌─────────────────────────┐
                    │   browserCtx (Chrome)   │
                    │  (processo compartilhado)│
                    └───────────┬─────────────┘
                                │
           ┌────────────────────┼────────────────────┐
           │                    │                    │
           ▼                    ▼                    ▼
     worker tab 1         worker tab 2         worker tab N
     (chromedp)           (chromedp)           (chromedp)
           │                    │                    │
           ▼                    ▼                    ▼
     open place URL       open place URL       open place URL
     extract details      extract details      extract details
           │                    │                    │
           └────────────────────┼────────────────────┘
                                ▼
                         results channel
                                ▼
                      lista ordenada de Place
```

#### Como o paralelismo funciona

1. Canal `jobs` recebe `{index, url}` de cada link coletado.
2. `enrichWorkers` goroutines consomem o canal.
3. Cada worker cria uma **tab filha** de `browserCtx` (mesmo Chrome, várias abas).
4. O worker navega até o place e extrai:
   - nome, telefone, endereço, website, categoria, rating, foto
   - `lat` / `lng` (preferencialmente de `!3dLAT!4dLNG` na URL)
   - `id` (`ChIJ...` ou `0x...:0x...`)
5. Resultados vão para um canal `results`.
6. Ao final, os places são remontados na ordem dos links e deduplicados.

Isso reduz bastante o tempo versus abrir 100 places um a um.

## Por que tabs filhas (e não browsers separados)?

- Mais leve (um processo Chrome).
- Compartilha o allocator/contexto raiz.
- Evita derrubar o browser ao fechar a aba do feed: a aba de busca é filha; os workers também são filhos do `browserCtx` vivo.

Cancelar o primeiro context do chromedp encerra o Chrome inteiro — por isso o ciclo de vida do browser é separado da aba de feed.

## Timeouts

- `TimeoutFor(limit)` estima duração com base em `limit` e `enrichWorkers`.
- O handler cria um `context.WithTimeout` com esse valor.
- O `WriteTimeout` do HTTP server fica desabilitado (`0`); quem controla o deadline é o context do scrape.

Clientes (Postman, Insomnia, curl, outros serviços) também precisam de timeout alto.

## Debug

Em falhas de navegação, bloqueio ou feed vazio, o scraper tenta salvar:

- `tmp/debug/maps_<timestamp>_<reason>.png`
- `tmp/debug/maps_<timestamp>_<reason>.html`

Esses arquivos ajudam a ver se houve cookie wall, CAPTCHA ou mudança de UI.

## Limitações conhecidas

- UI do Google Maps muda com frequência → seletores podem quebrar.
- CAPTCHA / detecção de bot pode interromper a coleta.
- `email` raramente aparece no Maps.
- O feed pode não chegar a 100 resultados para buscas estreitas.
- Paralelismo alto demais aumenta risco de bloqueio; `5` workers é um equilíbrio atual.

## Extensões possíveis

- Tornar `enrichWorkers` configurável por env/config.
- Cache de links/places por cidade+segmento.
- Fila assíncrona (request retorna `jobId`, outro endpoint consulta status).
- Fallback para Places API oficial (quando houver chave e budget).
