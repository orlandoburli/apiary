# Workflow: Correção Massiva de Testes Pré-Existentes

Quando o CI acumula **dezenas de testes quebrados** (migrations desatualizadas, lints acumulados, schemas alterados sem atualizar fixtures), a abordagem de "um PR por fix" é ineficiente porque cada PR individual não consegue passar no CI — os testes de OUTRO bug impedem o merge.

## Workflow: Branch Consolidado

### Quando usar
- 3+ testes/grupos de falha interdependentes (ex: migration quebra N testes que dependem dela)
- PRs individuais não conseguem passar no CI porque outros bugs pré-existentes impedem
- Workers ficam em crash loop "CI vermelho → não mergeia → CI continua vermelho"

### Passos

1. **Diagnosticar todas as falhas no CI** localmente:
   ```bash
   cd apps/api && go test ./... 2>&1 | grep -E "^FAIL|--- FAIL|panic" > /tmp/api-failures.txt
   cd apps/web && npx jest --no-cache 2>&1 | grep "FAIL" > /tmp/web-failures.txt
   cd apps/web && npm run lint 2>&1 | grep "error" > /tmp/lint-errors.txt
   ```

2. **Agrupar por causa raiz** (NÃO por arquivo de teste — vários testes podem compartilhar a mesma causa):
   - "fmt.Sprintf args mismatch" → 15 arquivos de teste
   - "NOT NULL violation" → 5 testes
   - "Coluna X não existe" → 3 testes

3. **Criar branch único** a partir da main mais recente:
   ```bash
   git checkout main && git pull --ff-only
   git worktree add ../project-erp--fix-all -b fix/all-fixes
   ```

4. **Usar delegate_task para paralelizar as correções**:
   - Cada subagente recebe UMA causa raiz (ex: "fix all fmt.Sprintf format-string mismatches")
   - Subagentes rodam em paralelo, cada um no mesmo worktree (cuidado com conflitos de arquivo)
   - Cada subagente deve rodar `go test ./...` na sua área APÓS o fix

5. **Verificar localmente ANTES do push**:
   ```bash
   cd apps/api && go clean -testcache && go test ./...  # 0 failures
   cd apps/web && npx jest --no-cache                    # 0 failures
   cd apps/web && npm run lint                           # 0 errors
   go vet ./apps/api/...                                 # 0 issues
   ```

6. **Commit único** com todos os fixes:
   ```bash
   git add -A && git commit -m "fix: ... N arquivos"
   git push origin fix/all-fixes
   ```

7. **Criar PR único** com descrição detalhada de cada grupo de fix.

8. **SEM auto-merge.** Acompanhar o CI. Se falhar, diagnosticar e repetir localmente. Só mergear quando CI ficar 100% verde.

### Vantagens
- Zero minutos de GitHub Actions desperdiçados em debug
- CI fica verde na primeira execução (porque tudo passou localmente)
- Um único PR para revisar em vez de N PRs interdependentes
- Histórico limpo (um commit, não 30 micro-fixes)

### Cuidados
- Não misturar correção de teste com mudança de lógica de produção
- Se um fix de produção for necessário (ex: função não usada que precisa ser removida), fazer em commit separado
- Rodar pre-push hooks (sqlc, tenant_id filters) antes do push final
