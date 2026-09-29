import { describe, expect, it } from 'vitest'
import { formatBytes, progressPercent } from './api'

describe('formatBytes', () => {
  it('formats binary units', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(1024)).toBe('1.0 KiB')
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.0 MiB')
  })
})

describe('progressPercent', () => {
  it('uses acknowledged bytes and stays within progress bar bounds', () => {
    expect(progressPercent(256, 1024)).toBe(25)
    expect(progressPercent(0, 0)).toBe(0)
    expect(progressPercent(1200, 1024)).toBe(100)
  })
})
