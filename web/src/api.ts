const storage = typeof localStorage === 'undefined' ? null : localStorage
let csrfToken = storage?.getItem('pv2_csrf') ?? ''

export function setCSRF(value: string) {
  csrfToken = value
  if (value) storage?.setItem('pv2_csrf', value)
  else storage?.removeItem('pv2_csrf')
}

export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  if (init.body && !(init.body instanceof Blob) && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  if (!['GET', 'HEAD'].includes((init.method ?? 'GET').toUpperCase()) && csrfToken) headers.set('X-CSRF-Token', csrfToken)
  const response = await fetch(`/api/v1${path}`, { ...init, headers, credentials: 'same-origin' })
  if (!response.ok) {
    const body = await response.json().catch(() => ({ error: response.statusText }))
    throw new Error(body.error ?? `HTTP ${response.status}`)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

export type NodeItem = {
  id: string; parent_id: string | null; name: string; kind: 'file' | 'folder'; size: number;
  state?: 'uploading' | 'queued' | 'processing' | 'ready' | 'failed'; error?: string;
  created_at: string; deleted_at?: string; purge_after?: string;
}

export function formatBytes(value: number): string {
  if (value === 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1)
  return `${(value / 1024 ** index).toFixed(index ? 1 : 0)} ${units[index]}`
}

export async function uploadFile(file: File, parentId: string | null, onProgress: (value: number) => void) {
  const resumeKey = `pv2_upload:${parentId ?? 'root'}:${file.name}:${file.size}:${file.lastModified}`
  let uploadId = storage?.getItem(resumeKey) ?? ''
  let offset = 0
  if (uploadId) {
    const status = await fetch(`/api/v1/uploads/${uploadId}`, { method: 'HEAD', credentials: 'same-origin' })
    if (status.ok) {
      offset = Number(status.headers.get('Upload-Offset') ?? 0)
      if (!Number.isSafeInteger(offset) || offset < 0 || offset > file.size) {
        uploadId = ''
        offset = 0
        storage?.removeItem(resumeKey)
      }
    } else {
      uploadId = ''
      storage?.removeItem(resumeKey)
    }
  }
  if (!uploadId) {
    const created = await api<{ upload_id: string }>('/uploads', { method: 'POST', body: JSON.stringify({ name: file.name, parent_id: parentId, size: file.size }) })
    uploadId = created.upload_id
    storage?.setItem(resumeKey, uploadId)
  }
  const partSize = 8 * 1024 * 1024
  onProgress(file.size ? offset / file.size : 1)
  while (offset < file.size) {
    const part = file.slice(offset, Math.min(file.size, offset + partSize))
    const response = await fetch(`/api/v1/uploads/${uploadId}`, {
      method: 'PATCH', credentials: 'same-origin', body: part,
      headers: { 'Upload-Offset': String(offset), 'Content-Type': 'application/offset+octet-stream', 'X-CSRF-Token': csrfToken },
    })
    if (!response.ok) throw new Error((await response.json().catch(() => ({}))).error ?? 'Upload failed')
    offset = Number(response.headers.get('Upload-Offset') ?? offset + part.size)
    onProgress(file.size ? offset / file.size : 1)
  }
  await api(`/uploads/${uploadId}/complete`, { method: 'POST' })
  storage?.removeItem(resumeKey)
}
