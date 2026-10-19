<p align="center"><img src="docs/assets/metricspire-mark.svg" width="44" height="44" alt="MetricSpire"></p>

<h1 align="center">MetricSpire</h1>

<p align="center"><strong>指標カタログとデータサービスを、MCP で AI につなぐ。</strong></p>

<p align="center"><a href="README.md">English</a> · <a href="README.zh-CN.md">简体中文</a> · <strong>日本語</strong></p>

MetricSpire は、指標カタログとデータサービスを提供するオープンソースプロジェクトです。指標の定義と版を一元管理し、HTTP API でデータクエリを提供し、MCP で AI アシスタントと接続します。

必要に応じて、承認済みのデータソース・フィールド・関連を定義した外部のセマンティック知識ベースを接続できます。これにより、一時的な分析結果をすべて固定指標として登録せずに、探索的分析を行えます。

> **開発プレビュー。** 自分のデータを問い合わせるには、設定済みのサービスとデータ権限が必要です。本番利用に対応した正式版はまだ公開していません。[現在の状況](docs/development-status.md)（英語）をご確認ください。

## 質問から結果までの例

公開の注文サンプルデータ（TPCH）を設定した環境では、AI アシスタントが次の質問を指標クエリに変換できます。

> サンプル注文 1–5 について、顧客セグメントと注文ステータス別に、売上・注文数・平均注文金額を調べてください。

MetricSpire は公開済みの定義に従って計算します。リポジトリの[期待結果](testdata/acceptance/databricks-tpch/expected.json)の先頭 2 行は次のとおりです。

| 顧客セグメント | 注文ステータス | 売上 | 注文数 | 平均注文金額 |
| --- | --- | ---: | ---: | ---: |
| BUILDING | F | 228570.52 | 1 | 228570.52 |
| BUILDING | O | 203010.51 | 1 | 203010.51 |

このサンプルでは、売上を対象注文の合計金額と定義しています。[指標定義](testdata/acceptance/databricks-tpch/model.yaml)と[構造化クエリ](testdata/acceptance/databricks-tpch/query.json)も確認できます。表は保存済みのテスト結果であり、ライブデモではありません。質問の解釈は AI クライアントが、構造化リクエストの検証と実行は MetricSpire が担当します。

## どこから始めますか？

| 目的 | 入口 | 必要なもの |
| --- | --- | --- |
| ローカル Codex CLI からチームのサービスを使う | [チーム用インストーラーを入手](docs/mcp-installer.md#get-the-installer) | 稼働中のインスタンス、チームのインストーラー、自分の Databricks アクセス権。 |
| 別の MCP クライアントを接続する | [MCP 接続ガイド](docs/mcp.md#connect) | インスタンスの URL と、その環境に対応した認証設定。 |
| データに接続せず指標定義を試す | [オフラインサンプル](docs/getting-started.md#compile-and-inspect-a-plan-without-a-database) | Go のみ。データベースやクラウドアカウントは不要です。生成されるのはクエリ計画であり、データの結果ではありません。 |
| チーム用のサービスを構築する | [サービス設定](docs/getting-started.md#run-the-http-service) | PostgreSQL、認証設定、分析エンジン。 |

Web アクセス権がある場合は、チームのインスタンスを開き、**指標カタログ**から始めてください。このリポジトリには公開サービスのインスタンスや社内の指標定義は含まれていません。詳細ガイドは現在英語です。

## データシステムでの役割

チームがレビュー済みの指標定義を既存のテーブルに接続します。MetricSpire はユーザーの権限を確認し、有効な定義を選び、クエリを実行して型付きの結果テーブルを返します。Web UI、HTTP API、MCP ツールは同じサービスを使います。管理者は定義の変更をレビューして公開し、履歴を残し、以前の版に戻せます。

**最初の分析エンジンは Databricks SQL です。** PostgreSQL には定義、版、監査記録を保存します。データの収集と加工は上流で必要です。MetricSpire はそのパイプラインやグラフダッシュボードを提供しません。AI モデルをホストせず、呼び出し側から任意の SQL も受け付けません。

探索的分析には、レビュー済みの構造化された意味定義カタログを使います。[外部の意味定義と設定](docs/exploration.md)をご覧ください。別途、PostgreSQL の事前集計データに対する[オンライン HTTP クエリ](docs/online-query.md)も有効にできます。それぞれに設定と受け入れ検証が必要です。

## ドキュメントと開発状況

- [はじめに](docs/getting-started.md) — オフラインサンプル、ローカルカタログ、サービス設定。
- [MCP 接続](docs/mcp.md) · [HTTP API](docs/http-api.md) — クライアントやアプリとの接続。
- [チーム設定](config/README.md) — 非公開設定、業務名の別名、レビュー済みの意味定義のエクスポート。
- [アーキテクチャ](docs/architecture.md) — 定義、権限、実行の境界。
- [開発状況](docs/development-status.md) — 実装済みの機能、検証の根拠、残っている課題。
- [貢献ガイド](CONTRIBUTING.md) · [ドキュメント一覧](docs/README.md)。

詳細ドキュメントは現在英語です。本番運用への対応、汎用的な複数エンジン分析、本番環境の遅延や可用性は保証されていません。試用指標には未検証の表示が残ります。意味定義の契約 [`metricspire.io/v1alpha1`](contracts/metricspire.schema.json) と予定する製品版 `v0.1.0` は別々にバージョン管理します。

[Apache 2.0](LICENSE) ライセンスです。公開サンプルは中立なデータを使います。社内定義、認証情報、本番データはこのリポジトリに含めないでください。
