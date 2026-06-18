import { ref } from 'vue'
import { readSidebarCollapsed, writeSidebarCollapsed } from '../config/appNav'

const collapsed = ref(readSidebarCollapsed())

export function useSidebar() {
  function toggleSidebar() {
    collapsed.value = !collapsed.value
    writeSidebarCollapsed(collapsed.value)
  }

  function setSidebarCollapsed(value: boolean) {
    collapsed.value = value
    writeSidebarCollapsed(value)
  }

  return { collapsed, toggleSidebar, setSidebarCollapsed }
}
