import * as monaco from '@codingame/monaco-vscode-editor-api'
import * as vscode from 'vscode'
import { initVimMode, type VimMode } from 'monaco-vim'
import {
  MonacoVscodeApiWrapper,
  type MonacoVscodeApiConfig,
} from 'monaco-languageclient/vscodeApiWrapper'
import { LanguageClientWrapper, type LanguageClientConfig } from 'monaco-languageclient/lcwrapper'
import { Worker as MlcWorker, useWorkerFactory } from 'monaco-languageclient/workerFactory'

declare global {
  interface Window {
    CookEditor: typeof CookEditor
    CookEditorDebug?: {
      triggerDefinition: () => void
      setPosition: (line: number, column?: number) => void
      revealLine: (line: number) => void
    }
    monaco: typeof monaco
  }
  interface WorkerGlobalScope {
    MonacoEnvironment?: {
      getWorkerUrl?: (moduleId: string, label: string) => string
      getWorkerOptions?: (moduleId: string, label: string) => WorkerOptions | undefined
    }
  }
}

const workerBaseUrl = new URL('workers/', import.meta.url)
const editorWorkerUrl = new URL('editor.worker.js', workerBaseUrl)

const workerLabels = [
  'editorWorkerService',
  'typescript',
  'javascript',
  'json',
  'css',
  'scss',
  'less',
  'html',
  'handlebars',
  'razor',
]

function configureWorkers() {
  const workerLoaders: Record<string, () => MlcWorker> = {}
  for (const label of workerLabels) {
    workerLoaders[label] = () => new MlcWorker(editorWorkerUrl, { type: 'classic' })
  }
  useWorkerFactory({ workerLoaders })
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
  if (lower.endsWith('.py')) return 'python'
  if (lower.endsWith('.toml')) return 'toml'
  if (lower.endsWith('.yaml') || lower.endsWith('.yml')) return 'yaml'
  if (lower.endsWith('.nix')) return 'nix'
  return 'plaintext'
}

export type OpenFileRequest = {
  uri: string
  lineNumber?: number
  column?: number
}

export type CookEditorInstance = {
  open: (opts: { filePath: string; uri: string; content: string; lspUrl?: string }) => void
  getValue: () => string
  setValue: (v: string) => void
  focus: () => void
  layout: () => void
  saveViewState: () => monaco.editor.ICodeEditorViewState | null
  restoreViewState: (state: monaco.editor.ICodeEditorViewState | null) => void
  dispose: () => void
  onDidChangeContent: (cb: () => void) => monaco.IDisposable
  onOpenFile: (cb: (request: OpenFileRequest) => void) => void
  setPosition: (lineNumber: number, column?: number) => void
  revealLine: (lineNumber: number) => void
  triggerGoToDefinition: () => void
  enableVim: (statusBarElement: HTMLElement) => void
  disableVim: () => void
  isVimEnabled: () => boolean
}

type LspStatus = 'connected' | 'connecting' | 'disconnected' | 'error'

type LspEntry = {
  key: string
  languageId: string
  lspUrl: string
  workspacePath: string
  wrapper: LanguageClientWrapper
  status: LspStatus
  startPromise?: Promise<void>
  listeners: Set<(status: LspStatus) => void>
}

let vscodeApiWrapper: MonacoVscodeApiWrapper | null = null
let vscodeApiInitPromise: Promise<void> | null = null
let vscodeWorkspacePath: string | null = null

let activeOpenFileHandler: ((request: OpenFileRequest) => void) | null = null

const lspEntries = new Map<string, LspEntry>()

function dispatchOpenFile(request: OpenFileRequest) {
  if (activeOpenFileHandler) {
    activeOpenFileHandler(request)
  } else {
    console.warn('[CookEditor] No open-file handler registered for:', request)
  }
}

function setActiveOpenFileHandler(handler: ((request: OpenFileRequest) => void) | null) {
  activeOpenFileHandler = handler
}

function makeWorkspaceFolder(workspacePath: string) {
  const name = workspacePath.split('/').filter(Boolean).pop() || 'workspace'
  return {
    index: 0,
    name,
    uri: vscode.Uri.file(workspacePath),
  }
}

async function ensureVscodeApi(workspacePath: string) {
  if (vscodeApiInitPromise) {
    if (vscodeWorkspacePath && vscodeWorkspacePath !== workspacePath) {
      console.warn('[CookEditor] VSCode API already initialized for:', vscodeWorkspacePath)
    }
    return vscodeApiInitPromise
  }

  vscodeWorkspacePath = workspacePath

  const config: MonacoVscodeApiConfig = {
    $type: 'classic',
    viewsConfig: {
      $type: 'EditorService',
      openEditorFunc: async (modelRef, options) => {
        try {
          const uri = modelRef?.object?.textEditorModel?.uri?.toString()
          if (!uri) return undefined
          const selection = (options as any)?.selection
          dispatchOpenFile({
            uri,
            lineNumber: selection?.startLineNumber,
            column: selection?.startColumn,
          })
        } finally {
          modelRef?.dispose()
        }
        return undefined
      },
    },
    workspaceConfig: {
      workspaceProvider: {
        trusted: true,
        workspace: {
          workspaceUri: vscode.Uri.file(workspacePath),
        },
        async open() {
          return true
        },
      },
    },
    monacoWorkerFactory: () => {
      configureWorkers()
    },
    advanced: {
      loadExtensionServices: false,
      loadThemes: false,
    },
  }

  vscodeApiWrapper = new MonacoVscodeApiWrapper(config)
  vscodeApiInitPromise = vscodeApiWrapper.start({ caller: 'CookEditor' })
  return vscodeApiInitPromise
}

function updateLspStatus(entry: LspEntry, status: LspStatus) {
  if (entry.status === status) return
  entry.status = status
  for (const listener of entry.listeners) {
    listener(status)
  }
}

function createLspEntry(languageId: string, lspUrl: string, workspacePath: string): LspEntry {
  const key = `${languageId}::${lspUrl}`
  const entry: LspEntry = {
    key,
    languageId,
    lspUrl,
    workspacePath,
    status: 'disconnected',
    wrapper: null as unknown as LanguageClientWrapper,
    listeners: new Set(),
  }

  const config: LanguageClientConfig = {
    languageId,
    connection: {
      options: {
        $type: 'WebSocketUrl',
        url: lspUrl,
        startOptions: {
          onCall: () => updateLspStatus(entry, 'connected'),
        },
        stopOptions: {
          onCall: () => updateLspStatus(entry, 'disconnected'),
        },
      },
    },
    clientOptions: {
      documentSelector: [{ language: languageId, scheme: 'file' }],
      workspaceFolder: makeWorkspaceFolder(workspacePath),
    },
  }

  entry.wrapper = new LanguageClientWrapper(config)
  return entry
}

async function startLsp(entry: LspEntry) {
  if (entry.startPromise) return entry.startPromise
  updateLspStatus(entry, 'connecting')
  entry.startPromise = entry.wrapper
    .start()
    .catch((err) => {
      console.error('[CookEditor] LSP start failed:', err)
      updateLspStatus(entry, 'error')
      entry.startPromise = undefined
      throw err
    })
    .then(() => {
      if (entry.status === 'connecting') {
        updateLspStatus(entry, 'connected')
      }
    })
  return entry.startPromise
}

function connectLsp(
  languageId: string,
  lspUrl: string,
  workspacePath: string,
  onStatus?: (status: LspStatus) => void
) {
  let entry = lspEntries.get(`${languageId}::${lspUrl}`)
  if (!entry) {
    entry = createLspEntry(languageId, lspUrl, workspacePath)
    lspEntries.set(entry.key, entry)
  }

  if (onStatus) {
    entry.listeners.add(onStatus)
  }

  const needsStart = !entry.startPromise
  if (needsStart && (entry.status === 'disconnected' || entry.status === 'error')) {
    updateLspStatus(entry, 'connecting')
  } else if (onStatus) {
    onStatus(entry.status)
  }

  void ensureVscodeApi(workspacePath)
    .then(() => startLsp(entry!))
    .catch((err) => {
      console.error('[CookEditor] VSCode API init failed:', err)
      updateLspStatus(entry!, 'error')
    })

  return () => {
    if (!onStatus) return
    entry?.listeners.delete(onStatus)
  }
}

export async function create(
  container: HTMLElement,
  workspacePath: string,
  onLspStatus?: (status: LspStatus) => void
): Promise<CookEditorInstance> {
  await ensureVscodeApi(workspacePath)

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

  window.monaco = monaco

  let openFileCallback: ((request: OpenFileRequest) => void) | null = null
  let lspStatusDispose: (() => void) | null = null
  let vimModeInstance: VimMode | null = null

  const triggerGoToDefinition = () => {
    editor.trigger('keyboard', 'editor.action.revealDefinition', {})
  }

  editor.addAction({
    id: 'cook.goToDefinition',
    label: 'Go to Definition',
    keybindings: [monaco.KeyMod.CtrlCmd | monaco.KeyMod.Alt | monaco.KeyCode.KeyD],
    run: triggerGoToDefinition,
  })

  editor.onDidFocusEditorText(() => {
    if (openFileCallback) setActiveOpenFileHandler(openFileCallback)
  })

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

    if (lspStatusDispose) {
      lspStatusDispose()
      lspStatusDispose = null
    }

    if (opts.lspUrl && languageId !== 'plaintext') {
      lspStatusDispose = connectLsp(languageId, opts.lspUrl, workspacePath, onLspStatus)
    } else {
      onLspStatus?.('disconnected')
    }
  }

  function onDidChangeContent(cb: () => void): monaco.IDisposable {
    return editor.onDidChangeModelContent(cb)
  }

  function setPosition(lineNumber: number, column?: number) {
    editor.setPosition({ lineNumber, column: column || 1 })
    editor.focus()
  }

  function revealLine(lineNumber: number) {
    editor.revealLineInCenter(lineNumber)
  }

  function enableVim(statusBarElement: HTMLElement) {
    if (vimModeInstance) return
    vimModeInstance = initVimMode(editor, statusBarElement)
  }

  function disableVim() {
    if (!vimModeInstance) return
    vimModeInstance.dispose()
    vimModeInstance = null
  }

  window.CookEditorDebug = {
    triggerDefinition: triggerGoToDefinition,
    setPosition,
    revealLine,
  }

  return {
    open,
    getValue: () => editor.getValue(),
    setValue: (v: string) => editor.setValue(v),
    focus: () => editor.focus(),
    layout: () => editor.layout(),
    saveViewState: () => editor.saveViewState(),
    restoreViewState: (state) => {
      if (state) editor.restoreViewState(state)
    },
    dispose: () => {
      if (lspStatusDispose) lspStatusDispose()
      if (openFileCallback && activeOpenFileHandler === openFileCallback) {
        setActiveOpenFileHandler(null)
      }
      disableVim()
      editor.dispose()
    },
    onDidChangeContent,
    onOpenFile: (cb: (request: OpenFileRequest) => void) => {
      openFileCallback = cb
      setActiveOpenFileHandler(cb)
    },
    setPosition,
    revealLine,
    triggerGoToDefinition,
    enableVim,
    disableVim,
    isVimEnabled: () => vimModeInstance !== null,
  }
}

export const version = '0.4.0'

const CookEditor = { create, version }
if (typeof window !== 'undefined') {
  window.CookEditor = CookEditor
}
export default CookEditor
