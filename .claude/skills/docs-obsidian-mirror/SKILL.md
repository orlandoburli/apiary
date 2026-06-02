---
name: docs-obsidian-mirror
description: Toda criação/edição de Markdown (docs/, openspec/, README) no repo deve ser espelhada como cópia idêntica no vault Obsidian em iCloud. Use sempre que criar, editar, mover ou apagar arquivo `.md` ou `.mdc` dentro do escopo (docs, openspec, README, AGENTS.md, CHANGELOG, CONTRIBUTING).
---

# Espelhamento de docs no Obsidian

Toda criação ou edição de documento Markdown no repositório **deve** ser replicada como cópia idêntica no vault Obsidian. Esta regra é temporária — vale até existir sincronização automática (a definir). Não pular.

## Caminhos

- **Repo (origem)**: `/Users/orlando/Projects/Personal/project-erp`
- **Vault (destino)**: `/Users/orlando/Library/Mobile Documents/iCloud~md~obsidian/Documents/Personal/Projects/Erp`

A estrutura de diretórios do destino **espelha** a do repo a partir da raiz. Exemplo: `docs/api/auth.md` no repo → `docs/api/auth.md` no vault.

## Escopo (o que espelhar)

Replicar qualquer arquivo `.md` ou `.mdc` dentro destes caminhos:

- `docs/**/*.md`
- `openspec/**/*.md` (specs vivas, changes, proposals, designs, tasks)
- `README.md` na raiz e qualquer `**/README.md` nos pacotes (`apps/*/README.md`, etc.)
- `CHANGELOG.md`, `CONTRIBUTING.md`, `AGENTS.md` na raiz quando existirem

**Não** espelhar:
- `node_modules/**`, `vendor/**`, `dist/**`, `build/**`, `.next/**`, qualquer artefato gerado
- `.cursor/**` (rules, skills, terminals — são metadados de tooling)
- `.github/**`
- `*.tsbuildinfo` ou similares
- Arquivos de teste/fixture binários ou snapshots

## Procedimento

Sempre que **criar ou editar** um arquivo no escopo acima, executar imediatamente após salvar:

```bash
SRC="/Users/orlando/Projects/Personal/project-erp"
DST="/Users/orlando/Library/Mobile Documents/iCloud~md~obsidian/Documents/Personal/Projects/Erp"
REL="<caminho relativo no repo, ex: openspec/changes/foo/proposal.md>"

mkdir -p "$DST/$(dirname "$REL")"
cp "$SRC/$REL" "$DST/$REL"
```

Para **deletar** um arquivo: remover também a cópia correspondente no vault (`rm "$DST/$REL"`) e qualquer diretório vazio resultante.

Para **renomear/mover**: deletar a cópia antiga no vault e copiar para o novo caminho.

## Quando espelhar em lote

Se a sessão criou/editou vários arquivos do escopo (ex: gerou um change OpenSpec inteiro com proposal + design + specs + tasks), espelhar todos no final da sessão com um único `rsync`:

```bash
rsync -av --delete \
  --include='*/' \
  --include='*.md' \
  --include='*.mdc' \
  --exclude='*' \
  "/Users/orlando/Projects/Personal/project-erp/openspec/" \
  "/Users/orlando/Library/Mobile Documents/iCloud~md~obsidian/Documents/Personal/Projects/Erp/openspec/"
```

Repetir para `docs/` e qualquer outro subpath afetado. **Não** rodar `rsync` na raiz inteira — o filtro precisa ser por subpath dentro do escopo, senão pode arrastar lixo.

## Validação rápida

Após espelhar, confirmar que pelo menos um arquivo recém-criado existe no destino:

```bash
ls -la "/Users/orlando/Library/Mobile Documents/iCloud~md~obsidian/Documents/Personal/Projects/Erp/<rel-path>"
```

Se der `No such file or directory`, refazer a cópia antes de seguir.

## Comportamento esperado do agente

1. Ao concluir qualquer tarefa que tocou em arquivo do escopo, **mencionar explicitamente** que espelhou no vault, listando os caminhos copiados.
2. Não pedir confirmação — espelhar é mandatório, não opcional.
3. Se a cópia falhar (ex: vault não montado pelo iCloud), avisar o usuário com a mensagem de erro exata e **não** prosseguir como se tivesse copiado.
