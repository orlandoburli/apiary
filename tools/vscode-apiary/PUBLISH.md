# Publishing Apiary Workflow Visualizer

This guide covers how the VS Code extension is published to the marketplace.

## Automatic Publishing (GitHub Actions)

The extension automatically publishes to the VS Code Marketplace whenever:
1. A change is pushed to `main` that modifies the extension source code
2. The version in `package.json` is bumped

**Workflow:** `.github/workflows/publish-vscode-extension.yml`
- Builds the extension
- Publishes to marketplace (using `VSCE_PAT` secret)
- Creates a GitHub release with the new version

### What Triggers Publication

Any push to `main` that changes:
- `tools/vscode-apiary/package.json` (especially version bump)
- `tools/vscode-apiary/src/**` (extension source code)
- `tools/vscode-apiary/package-lock.json` (dependencies)

## Manual Publishing

If you need to publish manually:

### Prerequisites

1. **Install vsce** (VS Code Extension Manager):
   ```bash
   npm install -g @vscode/vsce
   ```

2. **Have your VSCE_PAT** (Personal Access Token):
   - Generate at: https://dev.azure.com/orlandoburli/_usersSettings/tokens
   - Scopes needed: `Marketplace (publish)`, `Marketplace (manage)`
   - Store in a safe place (e.g., `.env.local`)

### Steps

1. **Update the version** in `package.json`:
   ```bash
   cd tools/vscode-apiary
   npm version minor  # or patch, major
   ```

2. **Build the extension**:
   ```bash
   npm run build
   ```

3. **Publish**:
   ```bash
   vsce publish -p <YOUR_VSCE_PAT>
   # or if VSCE_PAT is in environment:
   vsce publish
   ```

4. **Commit and push** the version bump:
   ```bash
   git add package.json package-lock.json
   git commit -m "chore(vscode): bump to v0.1.X"
   git push origin main
   ```

## Version Numbering

Use semantic versioning:
- **Major (0.X.0)**: Breaking changes to the extension API
- **Minor (0.1.X)**: New features (like schema validation in 0.1.3)
- **Patch (0.1.3)**: Bug fixes

## Marketplace Links

- **Extension Page**: https://marketplace.visualstudio.com/items?itemName=orlandoburli.vscode-apiary
- **Publisher Hub**: https://marketplace.visualstudio.com/manage/publishers/orlandoburli
- **Published Versions**: See the "Version History" tab on the marketplace page

## Troubleshooting

### "401 Unauthorized" when publishing

The `VSCE_PAT` secret is not set or has expired:
1. Generate a new token at https://dev.azure.com/orlandoburli/_usersSettings/tokens
2. Update the secret in GitHub: Settings → Secrets and variables → Actions → `VSCE_PAT`

### Build fails

```bash
cd tools/vscode-apiary
npm install
npm run build
```

Check for TypeScript errors:
```bash
npx tsc --noEmit
```

### Extension doesn't appear in marketplace after publishing

- Wait 5-10 minutes (marketplace caches)
- Check the Hub URL to verify it was published
- Check GitHub Actions logs for any errors

## Related Changes

When publishing a new version:
1. Update `version` in `package.json`
2. Optionally update `CHANGELOG.md` with release notes
3. Commit and push to main
4. GitHub Actions automatically publishes

No manual marketplace interaction needed beyond the token setup!
