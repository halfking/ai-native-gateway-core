// FocusMachine — UI规范 07 §8/§9 专注工作区状态机。
// normal → entering → focused → exiting → normal；进入前保存快照、登记
// presentation='focus'、再锁背景；退出逆序，任一步失败清理锁与注册。

export type FocusState = 'normal' | 'entering' | 'focused' | 'exiting'

export class FocusMachine {
  private _state: FocusState = 'normal'

  get state(): FocusState {
    return this._state
  }

  /**
   * @param apply 进入副作用（登记 overlay、Teleport 激活、锁背景滚动、inert）
   * @param rollback entering 阶段失败的清理（07 §9：任一步失败清理锁与注册）
   */
  async enter(apply: () => void | Promise<void>, rollback: () => void): Promise<boolean> {
    if (this._state !== 'normal') return false
    this._state = 'entering'
    try {
      await apply()
      this._state = 'focused'
      return true
    } catch {
      rollback()
      this._state = 'normal'
      return false
    }
  }

  async exit(restore: () => void | Promise<void>): Promise<boolean> {
    if (this._state !== 'focused') return false
    this._state = 'exiting'
    try {
      await restore()
    } finally {
      this._state = 'normal'
    }
    return true
  }

  reset(): void {
    this._state = 'normal'
  }
}
