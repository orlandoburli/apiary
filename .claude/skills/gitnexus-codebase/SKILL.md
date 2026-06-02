---
name: gitnexus-codebase
description: Consultar GitNexus (knowledge graph com clusters, processos e relações) ANTES de modificar ou investigar código. Use ao iniciar qualquer tarefa de código — `query` para encontrar contexto, `context` para visão 360° de um símbolo, `impact` para blast radius, `detect_changes` antes de commitar.
---

# GitNexus — consultar antes de mexer no código

O repositório está indexado no GitNexus (knowledge graph com clusters, fluxos e relações entre símbolos). **Sempre** usar as ferramentas MCP do GitNexus como primeiro passo antes de ler ou modificar código.

## Quando consultar

- Antes de implementar qualquer change, feature ou fix
- Antes de investigar um bug ou entender um fluxo
- Antes de refatorar ou renomear símbolos
- Ao precisar entender dependências ou blast radius de uma mudança

## Como consultar

1. **`query`** — busca híbrida (BM25 + semântica) para encontrar código relevante por descrição ou termo
2. **`context`** — visão 360° de um símbolo (referências, participação em processos, cluster)
3. **`impact`** — análise de blast radius com scores de confiança e profundidade
4. **`detect_changes`** — mapeia linhas alteradas para processos afetados (útil pré-commit)
5. **`rename`** — renomeação coordenada multi-arquivo via grafo + busca textual
6. **`cypher`** — queries Cypher diretas contra o grafo KuzuDB
7. **`list_repos`** — lista repositórios indexados

## Recursos (reads leves, 100-500 tokens)

- `gitnexus://repos` — lista repos indexados
- `gitnexus://repo/{name}/context` — stats e ferramentas disponíveis
- `gitnexus://repo/{name}/clusters` — clusters funcionais com scores de coesão
- `gitnexus://repo/{name}/processes` — fluxos de execução

## Fluxo esperado

1. Recebeu tarefa de código → **query** no GitNexus para entender o contexto
2. Identificou símbolo/módulo relevante → **context** para ver referências e dependências
3. Vai modificar algo → **impact** para avaliar blast radius
4. Só depois de ter o contexto do grafo → abrir arquivos e implementar

## Importante

- **Não pular** a consulta ao GitNexus. O grafo tem informações estruturais (clusters, processos, dependências) que leitura direta de arquivos não fornece.
- Se o índice estiver desatualizado (staleness check via `gitnexus://repo/{name}/context`), rodar `gitnexus analyze --skip-agents-md` no repo antes de prosseguir.
- O repo no GitNexus se chama `project-erp`.