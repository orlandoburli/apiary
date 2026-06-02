---
name: handler-error-logging
description: Handlers HTTP em Go (`apps/api/**/handler*.go`) devem logar o erro real com `log.Error().Err(err)` antes de devolver 500 — nunca engolir o erro. Mensagem do log prefixada com nome do módulo, mensagem HTTP genérica para o cliente. Use ao criar ou editar handlers Go que retornam erro.
---

# Logging obrigatório em erros 500

Todo handler HTTP que devolver `500 Internal Server Error` **deve** logar o erro real antes de responder ao cliente. Nunca engolir o erro.

## Funções `handleError` / `handleServiceError`

O `default:` de qualquer switch de erro deve logar antes de responder:

```go
// ❌ ERRADO — erro engolido, 500 opaco nos logs
default:
    response.Error(w, http.StatusInternalServerError, "erro interno")

// ✅ CERTO — causa raiz aparece no log estruturado
default:
    log.Error().Err(err).Msg("modulo: erro interno não classificado")
    response.Error(w, http.StatusInternalServerError, "erro interno")
```

Import necessário: `"github.com/rs/zerolog/log"`.

## Handlers inline (sem função centralizada)

Quando o erro é tratado direto no handler sem passar por `handleError`:

```go
// ❌ ERRADO
if err != nil {
    response.Error(w, http.StatusInternalServerError, "erro ao listar X")
    return
}

// ✅ CERTO
if err != nil {
    log.Error().Err(err).Msg("modulo: erro ao listar X")
    response.Error(w, http.StatusInternalServerError, "erro ao listar X")
    return
}
```

## Regras

1. **Nunca** devolver 500 sem `log.Error().Err(err)` antes.
2. A mensagem do log deve incluir o nome do módulo como prefixo (ex: `"caixa: ..."`, `"vendas: ..."`).
3. A mensagem HTTP para o cliente continua genérica (`"erro interno"`) — não vazar detalhes de implementação.
4. O padrão de referência está em `apps/api/internal/compras/handler.go`.
