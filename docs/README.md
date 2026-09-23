# docs/

```
docs/
├── brief/
│   └── MASTER_PROMPT.md       полный бриф проекта (части A–D) — единый источник требований
├── design/
│   └── CLAUDE_DESIGN_PROMPT.md  задание для Claude Design: что делать, куда класть, как сдавать
├── ops/                       эксплуатация: DNS, почта, Search Console, runbooks (фаза 1+)
├── architecture.md            архитектурные решения и их обоснование
├── contract.md                контракт дизайн ↔ бэкенд: схемы данных, слоты, трекинг
├── integration-notes.md       журнал правок в design/, сделанных при интеграции
└── netmap.md                  карта интернета /map: источники, размеры, модель, форматы, API
```

Приоритет документов при расхождении: `contract.md` → `architecture.md` → `brief/MASTER_PROMPT.md`. Более поздние решения записаны в первых двух.
