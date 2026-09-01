import { describe, expect, it } from 'vitest'
import { formatBytes } from './api'

describe('formatBytes', () => {
  it('formats binary units', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(1024)).toBe('1.0 KiB')
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.0 MiB')
  })
})

