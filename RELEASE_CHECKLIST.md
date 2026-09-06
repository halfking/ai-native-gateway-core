# Open Source Release Checklist

This checklist must be completed before pushing to public GitHub repository.

## ✅ Phase 1: Security & Privacy (COMPLETED)

- [x] Removed tracked environment encryption files from Git
- [x] Updated .gitignore to block all *.enc files
- [x] Ran strict secret scanner - 0 BLOCK-level findings in public docs
- [x] Created NOTICE file with Apache 2.0 attribution
- [x] Verified no internal domains in public documentation
- [x] Verified no real credentials in code or configs

## ✅ Phase 2: Community Infrastructure (COMPLETED)

- [x] Created CODE_OF_CONDUCT.md (Contributor Covenant)
- [x] Created SUPPORT.md (help channels and bug reporting)
- [x] Created NOTICE (Apache 2.0 attribution)
- [x] Updated SECURITY.md (removed internal contacts, kept GitHub reporting)
- [x] Updated CONTRIBUTING.md (removed internal references)
- [x] Created GitHub issue templates (bug report, feature request)
- [x] Created GitHub Actions workflows (security scan, release)

## ✅ Phase 3: Deployment Experience (COMPLETED)

- [x] Created docker-compose.quickstart.yml (self-contained stack)
- [x] Created .env.quickstart.example (with key generation commands)
- [x] Created docs/getting-started.md (10-minute quick start)
- [x] Validated Docker Compose configuration
- [x] Tested Go build successfully

## ✅ Phase 4: Documentation (COMPLETED)

- [x] Rewrote README.md for public audience
- [x] Created docs/architecture.md (Mermaid diagrams, tech stack)
- [x] Created docs/comparison.md (vs LiteLLM, Portkey, Kong)
- [x] Created ROADMAP.md (Now/Next/Exploring structure)
- [x] Removed production metrics and unverifiable claims
- [x] Marked K8s as "test-grade, not production-validated"
- [x] Marked shadow features clearly (cost/quality scoring, Session V2)

## ⚠️ Phase 5: Screenshots (MANUAL REVIEW REQUIRED)

- [x] Created docs/SCREENSHOT_GUIDE.md (capture instructions)
- [x] Created docs/assets/SCREENSHOT_AUDIT.md (audit criteria)
- [ ] **MANUAL**: Review existing screenshots for sensitive data
- [ ] **MANUAL**: Regenerate screenshots with demo data OR
- [ ] **MANUAL**: Remove existing screenshots until safe ones generated

**Current Status**: No screenshots approved for public release yet.

**Options**:
1. Ship without screenshots initially
2. Manually audit and scrub existing screenshots
3. Generate new screenshots from local demo environment

## ✅ Phase 6: Build & Test Validation (COMPLETED)

- [x] Go build succeeds
- [x] Core tests pass
- [x] Docker Compose config validates
- [x] No internal domains in public docs
- [x] GitHub Actions workflows created

## 📋 Phase 7: Pre-Push Final Verification (TODO)

Before `git push origin main`:

### Security Scan
```bash
bash scripts/scan-secrets.sh --mode=strict
# Expected: 0 BLOCK findings, acceptable WARN count
```

### Git Status
```bash
git status
# Verify removed files are not staged
# Verify new files are staged
```

### Staged Changes Review
```bash
git diff --cached --stat
# Expected adds:
# - NOTICE, CODE_OF_CONDUCT.md, SUPPORT.md
# - docker-compose.quickstart.yml, .env.quickstart.example
# - docs/getting-started.md, docs/architecture.md, docs/comparison.md
# - ROADMAP.md, RELEASE_CHECKLIST.md
# - .github/workflows/*.yml, .github/ISSUE_TEMPLATE/*.md
# Expected removes:
# - .env.*.enc (4 files)
# Expected modifies:
# - .gitignore, README.md, SECURITY.md, CONTRIBUTING.md
```

### Documentation Links
```bash
# Verify all markdown links resolve
find docs -name "*.md" -exec grep -H "\[.*\](" {} \; | grep -v "^Binary"
```

### Commit Message
```
feat: prepare repository for public open source release

Major changes:
- Remove environment encryption files from tracking
- Add community governance files (CODE_OF_CONDUCT, SUPPORT, NOTICE)
- Create public-friendly quick start (docker-compose.quickstart.yml)
- Rewrite README, architecture, comparison, and roadmap for public audience
- Update SECURITY and CONTRIBUTING to remove internal references
- Add GitHub Actions workflows for security scanning and releases
- Create screenshot capture guide and audit checklist

BREAKING: This commit removes several internal deployment artifacts
from public visibility. Private deployment materials should be maintained
in a separate internal repository.

Closes #XXX (if applicable)
```

## 📋 Phase 8: Post-Push Verification (TODO)

After pushing to GitHub:

### Repository Settings

- [ ] Enable GitHub Discussions
- [ ] Enable GitHub Security Advisories
- [ ] Configure branch protection for `main`
- [ ] Set up repository topics/tags
- [ ] Add repository description
- [ ] Set repository homepage URL (if applicable)
- [ ] Review and enable/disable GitHub features (Wiki, Projects, etc.)

### Documentation

- [ ] Verify README renders correctly on GitHub
- [ ] Verify all documentation links work
- [ ] Verify Mermaid diagrams render
- [ ] Check mobile/responsive view

### Community Files

- [ ] Verify GitHub shows "Community Standards" as complete
- [ ] Verify license badge appears correctly
- [ ] Test issue template rendering
- [ ] Test security advisory workflow

### CI/CD

- [ ] Verify GitHub Actions run successfully
- [ ] Check secret scanning alerts (should be 0)
- [ ] Verify release workflow is ready (but don't trigger yet)

## 🚀 Phase 9: Public Announcement (TODO)

### Before Announcing

- [ ] Tag initial release (e.g., v2.0.0)
- [ ] Write release notes
- [ ] Prepare announcement text
- [ ] Identify announcement channels

### Announcement Channels

- [ ] GitHub Discussions (pin announcement)
- [ ] Social media (if applicable)
- [ ] Developer communities (Reddit, HN, etc. - if appropriate)
- [ ] Documentation site (if exists)

### Announcement Template

```markdown
# AI Native Gateway - Now Open Source! 🎉

We're excited to announce that AI Native Gateway is now open source under Apache 2.0!

## What is AI Native Gateway?

Private-deployment LLM gateway with intelligent routing, multi-tenancy, and 
comprehensive observability. Built with Go + PostgreSQL + Redis.

## Key Features

- 🔒 100% private deployment (no SaaS dependencies)
- 🎯 Intelligent routing with sticky sessions
- 👥 Deep multi-tenancy (PostgreSQL RLS)
- 📊 Real-time observability and cost tracking
- 🔌 OpenAI, Anthropic, Gemini compatibility

## Quick Start

[10-minute Docker Compose setup](https://github.com/halfking/ai-native-gateway-core)

## Links

- Repository: https://github.com/halfking/ai-native-gateway-core
- Documentation: [docs/](https://github.com/halfking/ai-native-gateway-core/tree/main/docs)
- Comparison: [vs LiteLLM, Portkey, Kong](docs/comparison.md)

We welcome contributions and feedback!
```

## ❗ Known Limitations for Initial Release

Document these clearly in README or separate file:

1. **Screenshots**: Not included in initial release; will be added after demo environment setup
2. **Kubernetes**: Manifests are test-grade, not production-validated
3. **Documentation Gaps**: Some advanced features lack detailed guides
4. **CI/CD**: Release artifacts not yet published to package registries
5. **Localization**: Documentation is English-first; i18n support limited

## 📝 Post-Release Roadmap

Priority items for first 30 days:

1. Generate and add safe screenshots
2. Set up automated Docker image builds
3. Create video walkthrough or demo
4. Write troubleshooting guide
5. Expand architecture documentation
6. Set up Discussions categories
7. Create first "good first issue" labels
8. Write contributor onboarding guide

---

**Status Summary**: 
- ✅ 6/9 phases complete
- ⚠️ 1 phase needs manual work (screenshots)
- 📋 2 phases pending (final push, post-push verification)

**Estimated Time to Complete**: 
- Manual screenshot review: 1-2 hours
- Final verification: 30 minutes
- Post-push setup: 30 minutes

**Blocker**: Screenshots require manual review or regeneration with demo data.

**Recommendation**: Ship initial release without screenshots, add them in follow-up PR after safe ones are generated.
