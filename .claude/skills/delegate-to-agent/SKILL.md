# Delegate to Agent

Cria uma issue no Plane e delega a execução para o agente correto do pipeline.

**Use este skill sempre que:**
- O usuário pedir para implementar algo (`agent:engineer`)
- O usuário quiser analisar, planejar ou decompor uma demanda complexa (`agent:staff`)
- O usuário pedir análise de produto, specs, critérios de aceite (`agent:po`)
- O usuário quiser validar, testar ou rodar regressão (`agent:qa`)
- A demanda for ambígua e precisar de triagem (`agent:investigator` — sem label)

## Como usar

```
/delegate-to-agent [descrição da demanda]
```

Exemplos:
- `/delegate-to-agent Adicionar campo GTIN na busca do PDV`
- `/delegate-to-agent Revisar spec de compras e verificar se está implementada`
- `/delegate-to-agent Validar se o fluxo de cotação está funcionando corretamente`

## Passos

1. **Determine o tipo de agente** baseado na natureza da demanda:
   - Implementação técnica clara → `agent:engineer`
   - Demanda complexa / multi-módulo / arquitetural → `agent:staff`
   - Análise de produto, specs, documentação → `agent:po`
   - Validação, testes, regressão → `agent:qa`
   - Ambíguo → sem label (investigador vai classificar)

2. **Crie a issue no Plane** via API:

```bash
source /Users/orlando/Projects/Personal/project-erp/tools/agent-dispatcher/.env

# Cria a issue
RESP=$(curl -s -X POST \
  -H "X-Api-Key: $PLANE_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{
    \"name\": \"<TITULO>\",
    \"description_html\": \"<DESCRICAO_HTML>\",
    \"priority\": \"medium\"
  }" \
  "$PLANE_URL/api/v1/workspaces/$PLANE_WORKSPACE/projects/$PLANE_PROJECT/issues/")

ISSUE_ID=$(echo $RESP | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])")
echo "Issue criada: $ISSUE_ID"

# Adiciona a label correta
LABEL_ID=$(curl -s \
  -H "X-Api-Key: $PLANE_TOKEN" \
  "$PLANE_URL/api/v1/workspaces/$PLANE_WORKSPACE/projects/$PLANE_PROJECT/labels/" \
  | python3 -c "import sys,json; labels=json.load(sys.stdin)['results']; print(next(l['id'] for l in labels if l['name']=='<LABEL_NAME>'))")

curl -s -X POST \
  -H "X-Api-Key: $PLANE_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"label\": \"$LABEL_ID\"}" \
  "$PLANE_URL/api/v1/workspaces/$PLANE_WORKSPACE/projects/$PLANE_PROJECT/issues/$ISSUE_ID/issue-labels/"
```

3. **Dispara o dispatcher** imediatamente:

```bash
curl -s -X POST "http://localhost:8090/trigger?task_id=$ISSUE_ID" | python3 -m json.tool
```

4. **Informe o usuário** com:
   - Link da issue no Plane: `http://localhost:8091/$PLANE_WORKSPACE/projects/$PLANE_PROJECT/issues/$ISSUE_ID/`
   - Qual agente foi designado
   - Como acompanhar: `tail -f /tmp/agent-<tipo>-<id[:8]>.log`

## Regras

- SEMPRE criar a issue antes de qualquer implementação direta
- NUNCA implementar diretamente quando a demanda couber num agente do pipeline
- Se a demanda for urgente e simples (< 5 min), pode implementar diretamente e mencionar que não foi delegado
- Prioridade padrão: `medium`. Ajuste para `urgent` se o usuário indicar urgência
- Título deve ser em português, conciso e acionável
- Descrição deve ter: contexto, objetivo e critérios de aceite mínimos
