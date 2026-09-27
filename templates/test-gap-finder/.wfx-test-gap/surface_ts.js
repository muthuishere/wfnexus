// surface_ts.js — the TypeScript/JavaScript half of surface.py.
// Uses the REPO's own `typescript` package (node_modules), so the parse is the
// one the project compiles with. Prints {file: [items]} as JSON.
//
// Items: exported/public functions, classes, methods; if/else, ternaries,
// switch cases, throw, try/catch, loops, `??` / `?.` / `||` defaults (the
// null/undefined paths), await/Promise .then/.catch (async error paths), and
// comparisons against literals (boundaries).
const path = require('path')
let ts
try {
  ts = require(path.resolve('node_modules/typescript'))
} catch (e) {
  console.error('typescript is not installed in this repo (node_modules/typescript): ' + e.message)
  process.exit(2)
}
const fs = require('fs')

const out = {}
for (const file of process.argv.slice(2)) {
  const text = fs.readFileSync(file, 'utf8')
  const sf = ts.createSourceFile(file, text, ts.ScriptTarget.Latest, true)
  const items = []
  const line = n => sf.getLineAndCharacterOfPosition(n.getStart(sf)).line + 1
  const src = n => n.getText(sf).split('\n')[0].slice(0, 160)
  const isExported = n => (ts.getCombinedModifierFlags(n) & ts.ModifierFlags.Export) !== 0
  const isPrivate = n => {
    const f = ts.getCombinedModifierFlags(n)
    return (f & (ts.ModifierFlags.Private | ts.ModifierFlags.Protected)) !== 0 || (n.name && ts.isPrivateIdentifier(n.name))
  }
  const params = fn => (fn.parameters || []).map(p => p.getText(sf).slice(0, 60)).join(', ')

  function inner(node, owner) {
    const visit = n => {
      const L = line(n)
      if (ts.isIfStatement(n)) {
        items.push({ kind: 'branch', line: L, symbol: owner, detail: 'if (' + src(n.expression) + ')' })
        if (n.elseStatement && !ts.isIfStatement(n.elseStatement))
          items.push({ kind: 'branch', line: line(n.elseStatement), symbol: owner, detail: 'else of if (' + src(n.expression) + ')' })
      } else if (ts.isConditionalExpression(n)) {
        items.push({ kind: 'branch', line: L, symbol: owner, detail: 'ternary ' + src(n) })
      } else if (ts.isCaseClause(n) || ts.isDefaultClause(n)) {
        items.push({ kind: 'branch', line: L, symbol: owner, detail: src(n) })
      } else if (ts.isThrowStatement(n)) {
        items.push({ kind: 'raise', line: L, symbol: owner, detail: src(n) })
      } else if (ts.isCatchClause(n)) {
        items.push({ kind: 'except', line: L, symbol: owner, detail: 'catch ' + src(n) })
      } else if (ts.isForStatement(n) || ts.isForOfStatement(n) || ts.isForInStatement(n) || ts.isWhileStatement(n) || ts.isDoStatement(n)) {
        items.push({ kind: 'loop', line: L, symbol: owner, detail: src(n) + ' — zero, one and many iterations' })
      } else if (ts.isBinaryExpression(n)) {
        const op = n.operatorToken.kind
        if (op === ts.SyntaxKind.QuestionQuestionToken || op === ts.SyntaxKind.BarBarToken || op === ts.SyntaxKind.AmpersandAmpersandToken) {
          items.push({ kind: 'null-path', line: L, symbol: owner, detail: src(n) + ' — null, undefined, falsy (0, "", false, NaN)' })
        } else if ([ts.SyntaxKind.EqualsEqualsToken, ts.SyntaxKind.EqualsEqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsToken,
          ts.SyntaxKind.ExclamationEqualsEqualsToken, ts.SyntaxKind.LessThanToken, ts.SyntaxKind.LessThanEqualsToken,
          ts.SyntaxKind.GreaterThanToken, ts.SyntaxKind.GreaterThanEqualsToken].includes(op) &&
          [n.left, n.right].some(x => ts.isLiteralExpression(x) || x.kind === ts.SyntaxKind.NullKeyword ||
            x.kind === ts.SyntaxKind.UndefinedKeyword || (ts.isIdentifier(x) && x.text === 'undefined'))) {
          items.push({ kind: 'boundary', line: L, symbol: owner, detail: src(n) + ' — at, just below/above, null vs undefined' })
        }
      } else if (ts.isPropertyAccessExpression(n) && n.questionDotToken) {
        items.push({ kind: 'null-path', line: L, symbol: owner, detail: src(n) + ' — optional chain on null/undefined' })
      } else if (ts.isAwaitExpression(n)) {
        items.push({ kind: 'async', line: L, symbol: owner, detail: src(n) + ' — resolved, rejected, non-promise value' })
      } else if (ts.isCallExpression(n) && ts.isPropertyAccessExpression(n.expression) &&
        ['then', 'catch', 'finally'].includes(n.expression.name.text)) {
        items.push({ kind: 'async', line: L, symbol: owner, detail: src(n) + ' — resolve and reject paths' })
      } else if (ts.isTypeOfExpression(n) || (ts.isBinaryExpression(n) && n.operatorToken.kind === ts.SyntaxKind.InstanceOfKeyword)) {
        items.push({ kind: 'branch', line: L, symbol: owner, detail: 'type check ' + src(n) })
      }
      if (n !== node && (ts.isFunctionDeclaration(n) || ts.isClassDeclaration(n))) return // counted on their own
      ts.forEachChild(n, visit)
    }
    ts.forEachChild(node, visit)
  }

  function fnItem(fn, name, kind) {
    items.push({ kind, line: line(fn), symbol: name,
      detail: `${name}(${params(fn)}) — every parameter: null, undefined, empty, zero/negative, huge, wrong type` })
    const doc = ts.getJSDocCommentsAndTags(fn).map(d => d.getText(sf)).join('\n').trim()
    if (doc) items.push({ kind: 'doc_claim', line: line(fn), symbol: name, detail: doc.slice(0, 300) })
    inner(fn, name)
  }

  const top = n => {
    if (ts.isFunctionDeclaration(n) && n.name) {
      fnItem(n, n.name.text, isExported(n) ? 'function' : 'internal-function')
    } else if (ts.isClassDeclaration(n) && n.name) {
      const cls = n.name.text
      items.push({ kind: 'class', line: line(n), symbol: cls, detail: `class ${cls}${isExported(n) ? ' (exported)' : ''} — construction, factories, chaining` })
      for (const m of n.members) {
        if ((ts.isMethodDeclaration(m) || ts.isGetAccessor(m) || ts.isSetAccessor(m) || ts.isConstructorDeclaration(m)) && !isPrivate(m)) {
          const nm = ts.isConstructorDeclaration(m) ? 'constructor' : m.name.getText(sf)
          const stat = (ts.getCombinedModifierFlags(m) & ts.ModifierFlags.Static) ? 'static ' : ''
          fnItem(m, `${cls}.${stat}${nm}`, 'method')
        } else if (ts.isPropertyDeclaration(m) && m.initializer && (ts.isArrowFunction(m.initializer) || ts.isFunctionExpression(m.initializer)) && !isPrivate(m)) {
          fnItem(m.initializer, `${cls}.${m.name.getText(sf)}`, 'method')
        }
      }
    } else if (ts.isVariableStatement(n)) {
      for (const d of n.declarationList.declarations) {
        if (d.initializer && (ts.isArrowFunction(d.initializer) || ts.isFunctionExpression(d.initializer)))
          fnItem(d.initializer, d.name.getText(sf), isExported(n) ? 'function' : 'internal-function')
      }
    } else if (ts.isExportDeclaration(n) || ts.isExportAssignment(n)) {
      items.push({ kind: 'export', line: line(n), symbol: src(n), detail: 'public entry point: ' + src(n) })
    }
  }
  sf.statements.forEach(top)
  out[file] = items
}
process.stdout.write(JSON.stringify(out))
