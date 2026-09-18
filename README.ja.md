<p align="center"><img src="docs/assets/metricspire-mark.svg" width="62" height="62" alt="MetricSpire のロゴ"></p>

<h1 align="center">MetricSpire</h1>

<p align="center"><strong>テーブルではなく、指標を問い合わせる。</strong></p>

<p align="center">レビュー済みの定義 · 制限付きクエリ · UI・API・MCP に共通の契約</p>

<p align="center"><a href="README.md">English</a> · <a href="README.zh-CN.md">简体中文</a> · <strong>日本語</strong></p>

<p align="center"><img src="docs/assets/semantic-workflow.svg" alt="公開 orders サンプルの概念図。売上指標を定義し、顧客の地域別に問い合わせ、認可と制限付き計画を経て実行します。"></p>

<p align="center"><sub>公開 <code>orders</code> モデルに基づく図解です。製品のスクリーンショットや実際のクエリ結果ではありません。</sub></p>

<p align="center"><a href="docs/getting-started.md">サンプルを試す</a> · <a href="docs/mcp.md">AI クライアントを接続</a> · <a href="docs/architecture.md">構成を読む</a></p>

MetricSpire はオープンソースのセマンティック指標サービスです。レビュー済みの指標を一度公開すれば、アプリや AI エージェントが指標コードとディメンションで同じガバナンス経路を利用できます。有効なリリースを解決し、方針と上限を適用したうえで、実データへのアクセス可否は分析エンジンに委ねます。

> **開発プレビューであり、本番利用を保証しません。** Databricks Apps の staging 経路は検証されていますが、予定している `v0.1.0` は未公開です。[検証済みの範囲](docs/development-status.md)（英語）をご覧ください。

## なぜ MetricSpire が必要か

呼び出し側は物理テーブルや無制限の SQL ではなく、**指標コードとディメンション**を指定します。MetricSpire はビジネス定義、レビュー済みのバインディング、有効な版、製品側の権限とクエリ上限を管理します。元データのアクセス権限は分析エンジンが判断します。

これは指標サービスであり、データウェアハウスや BI システム、エンジン固有の行・列レベルの権限管理を置き換えるものではありません。

## 実装済みの機能

- **管理されたカタログ：** PostgreSQL に楽観的リビジョン管理付きの下書き、検証、変更不能なリリース、有効版ポインター、ロールバック、履歴イベントを保存します。
- **移植可能な意味論：** 制約付きの式と論理計画を採用し、環境固有のテーブル・列の対応付けはレビュー済みの `SourceBinding` に分離します。呼び出し側から任意の SQL は受け付けません。
- **制御された実行：** 方針・エンジン能力の検査、決定的な計画、パラメーター化された Databricks SQL、タイムアウト、キャンセル、型付き結果、行数・バイト数の上限、失敗時に閉じる監査を備えます。
- **三つの入口と一つのサービス：** 管理・検索 UI、厳格な HTTP API、七つのリモート MCP クエリツールが同じアプリケーションサービスを利用します。
- **明確な認証境界：** セルフホストでは標準 OIDC、Databricks Apps ではプラットフォーム管理の ID を利用します。製品側の権限でウェアハウス固有のデータ権限を上書きすることはありません。

Databricks SQL は最初の分析エンジンアダプターです。現時点で複数エンジンの実行対応はうたっていません。PostgreSQL はトランザクション用の**制御プレーン**であり、分析ファクトや結果行は保存しません。詳しくは[アーキテクチャ](docs/architecture.md)と [HTTP 契約](docs/http-api.md)（英語）をご覧ください。

## セマンティック計画を試す

Go 1.26 または 1.27 が必要です。以下のオフライン例は中立的な注文モデルをコンパイルして計画を出力します。データベースは不要で、ウェアハウスへのクエリも送信しません。

```bash
mkdir -p dist/demo
go run ./cmd/metricspire compile \
  --source examples/orders/model.yaml \
  --out dist/demo/manifest.json
go run ./cmd/metricspire compile-policy \
  --source examples/orders/policy.yaml \
  --manifest dist/demo/manifest.json \
  --out dist/demo/policy-bundle.json
go run ./cmd/metricspire plan \
  --manifest dist/demo/manifest.json \
  --policy dist/demo/policy-bundle.json \
  --context examples/orders/context.json \
  --query examples/orders/query.json \
  --binding examples/orders/binding.json \
  --capabilities examples/orders/capabilities.json \
  --logical-out dist/demo/logical-plan.json \
  --physical-out dist/demo/physical-plan.json
```

例の context ファイルを信頼するのは**このオフラインデモに限ります**。ネットワーク経由の呼び出しで、ID、方針、バインディング、リリース、エンジン、SQL を利用者が指定することはできません。ローカル PostgreSQL の公開手順、サービス起動、検証は[はじめに](docs/getting-started.md)（英語）を参照してください。

## AI クライアントを接続する

デプロイ済みのインスタンスは `/api/v1/mcp` で Streamable HTTP MCP を公開します（Databricks Apps で推奨するパス）。クライアントは URL に接続するだけで、リポジトリのクローンやローカル実行は不要です。ツールは探索、説明、計画、制限付きクエリの送信、状態取得、キャンセルを提供し、**任意 SQL や公開操作は提供しません**。

```text
list_namespaces → search_metrics → explain_query → plan_query
               → submit_query → get_query / cancel_query
```

各リクエストは個別に認証されます。Databricks Apps では事前登録済みの公開 OAuth クライアントに加え、利用者自身の App とデータへの権限が必要です。ブラウザーでのログイン成功だけではクエリの検証にはなりません。[MCP 接続](docs/mcp.md)に設定と安全上の制約、[開発状況](docs/development-status.md)に検証済みの範囲を記載しています（英語）。

## ドキュメント

| 文書（英語） | 内容 |
| --- | --- |
| [はじめに](docs/getting-started.md) | オフライン計画、ローカルカタログ、サービス設定と検証。 |
| [MCP 接続](docs/mcp.md) | リモートツール、OAuth クライアント設定、ジョブの境界。 |
| [アーキテクチャ](docs/architecture.md) | 意味論、ID、制御プレーン、エンジンアダプター。 |
| [HTTP 契約](docs/http-api.md) | エンドポイント、権限、エラー、上限、監査。 |
| [開発状況](docs/development-status.md) | プレビュー段階の検証範囲、未完了項目、リリース条件。 |

[ドキュメント一覧](docs/README.md)では公開ガイドとメンテナー向け staging 手順を分けています。過去のフェーズ検証記録はローカルに保管します。

機械可読のセマンティック契約は [`metricspire.io/v1alpha1`](contracts/metricspire.schema.json) です。この契約の版は予定される製品版 `v0.1.0` とは独立しています。

## 対象範囲とライセンス

MetricSpire は ETL、LLM ホスティング、任意 SQL、巨大な結果のエクスポート、エンジン間 Join を提供しません。未対応の計画は実行前に失敗し、意味を黙って変えることはありません。

[Apache 2.0](LICENSE) の下で公開しています。中立的な公開サンプルを用いた独立した clean-room 実装です。認証情報、非公開の業務 Schema、本番データをコミットしないでください。
