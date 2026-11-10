import {afterEach,describe,it,expect,vi} from 'vitest'
import {api,setCSRF,scheduleSessionExpiry} from './api'
afterEach(()=>{setCSRF('');vi.useRealTimers();vi.unstubAllGlobals()})
describe('session expiration',()=>{
 it('expires at the absolute deadline and clears CSRF',()=>{vi.useFakeTimers();vi.setSystemTime(new Date('2026-11-11T00:00:00Z'));const reload=vi.fn(),setItem=vi.fn();vi.stubGlobal('location',{reload});vi.stubGlobal('sessionStorage',{setItem});setCSRF('fixture');const cancel=scheduleSessionExpiry('2026-11-12T00:00:00Z');vi.advanceTimersByTime(86400000-1);expect(reload).not.toHaveBeenCalled();vi.advanceTimersByTime(1);expect(reload).toHaveBeenCalledOnce();expect(setItem).not.toHaveBeenCalled();cancel()})
 it('does not reload repeatedly on initial anonymous 401',async()=>{const reload=vi.fn();vi.stubGlobal('location',{reload});vi.stubGlobal('fetch',vi.fn().mockResolvedValue(new Response('{"error":"unauthorized"}',{status:401})));await expect(api('/auth/session')).rejects.toThrow();expect(reload).not.toHaveBeenCalled()})
})
