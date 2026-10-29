<p align="center"><img src="docs/assets/metricspire-mark.svg" width="44" height="44" alt="MetricSpire"></p>
<h1 align="center">MetricSpire</h1>
<p align="center"><strong>指標を一度定義し、アプリと AI アシスタントから共通の意味で問い合わせる。</strong></p>
<p align="center"><a href="README.md">English</a> · <a href="README.zh-CN.md">简体中文</a> · <strong>日本語</strong></p>

MetricSpire は HTTP と MCP に対応したオープンソースの指標カタログ・データサービスです。チームが指標定義をレビューして公開し、アプリや AI アシスタントがその意味を確認して同じサービスからデータを取得します。

- **指標の管理**：ドラフト、定義レビュー、バージョン付き公開、Web ワークベンチ。
- **データの提供**：指標・期間・グループを指定する構造化リクエスト、アクセス確認、型付き結果。
- **必要に応じた探索**：レビュー済みの外部セマンティクスを読み込み、公開済み指標で答えられない一時的な分析を実行。

たとえば「決済成功注文数」を公開し、「先週、地域ごとの決済成功注文は何件？」と質問します。サービスは公開済みの定義と設定されたデータ経路を使います。アプリも HTTP で同じ指標を要求できます。

> **開発プレビュー。** 本番向けの正式リリースや公開デモサービスはありません。自社データの問い合わせにはデプロイ、認証、定義のレビューが必要です。[現状と検証範囲](docs/development-status.md)をご確認ください。

## Go だけで試す：データベース不要

Go 1.26 または 1.27 と Git を用意して実行します。

```sh
git clone https://github.com/marmot1024/metricspire.git
cd metricspire
sh examples/quickstart.sh
```

中立的な注文モデルとアクセス方針をコンパイルし、**日付・顧客セグメント別の平均注文額と返金率**の計画を表示します。新しい `dist/quickstart.*` ディレクトリに JSON ファイルを4つ作り、最後に次を表示します。

```text
Offline example complete. No database connection or data query was made.
```

実際のコンパイラとプランナーを使いますが、データベース接続やデータ取得は行いません。初回は Go の依存関係をダウンロードします。[各手順の説明](docs/getting-started.md#compile-and-inspect-a-plan-without-a-database)も参照できます。

## 目的に合わせて進む

| 目的 | 入口 | 必要なもの |
| --- | --- | --- |
| チームの既存サービスを使う | [Databricks インストーラー](docs/mcp-installer.md#get-the-installer) / [OIDC インストーラー](docs/oidc-mcp.md#user-installation)、[その他の MCP クライアント](docs/mcp.md#connect) | チームの URL、接続パッケージ、自分の権限。ソース取得は不要。 |
| 指標を追加・保守する | [定義 → ドラフト → 公開 → 問い合わせ](docs/metric-maintenance.md) · [任意の保守者 skill](skills/metricspire-maintainer/SKILL.md) | 合意済みの定義、モデル管理権限、既存データ経路。 |
| Databricks なしで導入する | [PostgreSQL + OIDC の手順](docs/self-hosted.md) | メタデータ／結果 DB、IdP、準備済み集計データ。利用者に Databricks アカウントは不要。 |
| 既存セマンティックレイヤーを接続する | [エクスポート → 設定 → 検出 → 照合](docs/semantic-layer.md) | レビュー済み AnalysisCatalog と対応探索エンジン。Git/dbt/Cube の自動取り込みは未実装。 |

Web にアクセスできる場合は、チームの**指標カタログ**から始めます。詳細ガイドは現在英語が中心で、3言語の概要は同じ機能範囲を説明しています。

## データ基盤での役割

既存パイプラインがデータを準備し、クエリエンジンが保存・計算します。MetricSpire は定義、アクセス確認、構造化クエリの提供を担当します。

![MetricSpire 構成：Web、HTTP、MCP クライアントからカタログとデータサービスへ接続し、アダプターがクエリエンジン、指標メタデータ、任意のセマンティクスを接続。](docs/assets/architecture.ja.svg)

| 役割 | 実装済みの選択肢 |
| --- | --- |
| 定義、公開履歴、監査 | PostgreSQL / MySQL 8.4 |
| 公開済み指標の分析クエリ | Databricks SQL |
| 事前集計結果の提供 | PostgreSQL / ClickHouse / Doris、HTTP・MCP 経由 |
| レビュー済み定義による一時分析 | Databricks、または Trino/Presto の制限付き単一テーブル DATE 探索 |

独立した OIDC デプロイでは Databricks 分析を無効化できます。DB の対応範囲は各行の役割に限られ、メタデータ用アダプターは汎用クエリ用ではありません。上流 ETL や業務テーブル作成は行いません。[対応範囲](docs/architecture.md#adapter-support)を参照してください。

## ドキュメントと貢献

- [全ドキュメント](docs/README.md) · [利用開始](docs/getting-started.md)
- [MCP ワークフロー](docs/mcp.md) · [HTTP API](docs/http-api.md)
- [非公開のチーム設定](config/README.md) · [実行エンジン](docs/runtime-engines.md)
- [更新と照合](docs/online-refresh.md) · [隔離環境での技術検証](deploy/testing/README.md)
- [アーキテクチャ](docs/architecture.md) · [開発状況](docs/development-status.md) · [貢献ガイド](CONTRIBUTING.md)

[Apache 2.0](LICENSE) ライセンスです。公開例は中立的なデータを使います。会社の定義、実際のフィールド対応、認証情報、本番データは非公開で管理してください。セマンティック契約 `metricspire.io/v1alpha1` と予定製品版 `v0.1.0` は別々にバージョン管理されます。
