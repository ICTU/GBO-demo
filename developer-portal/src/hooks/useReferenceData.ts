import { useEffect, useState } from 'react'
import { listCitizens, listOrganizations } from '../api/devClient'
import type { Citizen, Organization } from '../types'

export function useReferenceData() {
  const [citizens, setCitizens] = useState<Citizen[]>([])
  const [organizations, setOrganizations] = useState<Organization[]>([])
  useEffect(() => {
    void listCitizens().then(setCitizens).catch(() => {})
    void listOrganizations().then(setOrganizations).catch(() => {})
  }, [])
  return { citizens, organizations }
}
