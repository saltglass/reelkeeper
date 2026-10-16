package library

import "github.com/uptrace/bun"

// bunTx is a database transaction (named so the library's files read well).
type bunTx = bun.Tx

// bunSelect is a SELECT query, for WhereGroup callbacks.
type bunSelect = *bun.SelectQuery

// bunList wraps a slice for SQL "IN (?)".
func bunList(v any) bun.ListValues { return bun.List(v) }

// bunIDB is the database or a transaction: code that works in either.
type bunIDB = bun.IDB
