import * as monaco from 'monaco-editor'

declare global {
  interface Window {
    CookEditor: any
  }
  interface WorkerGlobalScope {
    MonacoEnvironment?: any
  }
}

const workerBase = '/static/editor/workers/'

;(self as unknown as WorkerGlobalScope).MonacoEnvironment = {
  getWorkerUrl: function (_moduleId: string, label: string) {
    switch (label) {
      case 'json':
        return workerBase + 'json.worker.js'
      case 'css':
      case 'scss':
      case 'less':
        return workerBase + 'css.worker.js'
      case 'html':
      case 'handlebars':
      case 'razor':
        return workerBase + 'html.worker.js'
      case 'typescript':
      case 'javascript':
        return workerBase + 'ts.worker.js'
      default:
        return workerBase + 'editor.worker.js'
    }
  },
}

function inferLanguageId(filePath: string): string {
  const lower = filePath.toLowerCase()
  if (lower.endsWith('.go')) return 'go'
  if (lower.endsWith('.rs')) return 'rust'
  if (lower.endsWith('.ts') || lower.endsWith('.tsx')) return 'typescript'
  if (lower.endsWith('.js') || lower.endsWith('.jsx')) return 'javascript'
  if (lower.endsWith('.json')) return 'json'
  if (lower.endsWith('.md')) return 'markdown'
  if (lower.endsWith('.html') || lower.endsWith('.htm')) return 'html'
  if (lower.endsWith('.css')) return 'css'
  if (lower.endsWith('.toml')) return 'toml'
  if (lower.endsWith('.yaml') || lower.endsWith('.yml')) return 'yaml'
  return 'plaintext'
}

export type CookEditorInstance = {
  open: (opts: { filePath: string; uri: string; content: string; lspUrl?: string }) => void
  getValue: () => string
  setValue: (v: string) => void
  focus: () => void
  layout: () => void
  saveViewState: () => any
  restoreViewState: (state: any) => void
  dispose: () => void
  onDidChangeContent: (cb: () => void) => void
}

export function create(container: HTMLElement, workspacePath: string, onLspStatus?: (s: string) => void): CookEditorInstance {
  const editor = monaco.editor.create(container, {
    value: '',
    language: 'plaintext',
    theme: 'vs-dark',
    minimap: { enabled: false },
    automaticLayout: true,
    scrollBeyondLastLine: false,
    fontFamily: 'Menlo, Monaco, "Courier New", monospace',
    fontSize: 14,
  })

  // No LSP for now - mark as disconnected
  onLspStatus?.('disconnected')

  function open(opts: { filePath: string; uri: string; content: string; lspUrl?: string }) {
    const languageId = inferLanguageId(opts.filePath)
    const modelUri = monaco.Uri.parse(opts.uri)
    let model = monaco.editor.getModel(modelUri)
    if (!model) {
      model = monaco.editor.createModel(opts.content, languageId, modelUri)
    } else {
      model.setValue(opts.content)
      monaco.editor.setModelLanguage(model, languageId)
    }
    editor.setModel(model)
    editor.focus()
  }

  function onDidChangeContent(cb: () => void) {
    editor.onDidChangeModelContent(cb)
  }

  return {
    open,
    getValue: () => editor.getValue(),
    setValue: (v: string) => editor.setValue(v),
    focus: () => editor.focus(),
    layout: () => editor.layout(),
    saveViewState: () => editor.saveViewState(),
    restoreViewState: (state: any) => editor.restoreViewState(state),
    dispose: () => editor.dispose(),
    onDidChangeContent,
  }
}

export const version = '0.1.0'
